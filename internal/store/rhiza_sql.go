package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mrchypark/rhiza"
)

type rhizaSQL struct {
	db      *rhiza.DB
	trust   *trustAnchor
	trustMu sync.Mutex
}

type rhizaRows struct {
	rows  [][]any
	index int
	err   error
}

type rhizaRow struct {
	row []any
	err error
}

func openRhiza(ctx context.Context, config rhiza.Config) (*rhizaSQL, error) {
	db, err := rhiza.Open(ctx, config)
	if err != nil {
		return nil, err
	}
	return &rhizaSQL{db: db}, nil
}

func (d *rhizaSQL) Close() error { return d.db.Close() }

func (d *rhizaSQL) WaitReady(ctx context.Context) error {
	return waitUntilReadable(ctx, d.db.Ready, func(ctx context.Context) error {
		_, err := d.db.Query(ctx, rhiza.QueryRequest{SQL: "SELECT 1", Consistency: rhiza.ConsistencyLinearizable})
		return err
	})
}

func waitUntilReadable(ctx context.Context, ready func() bool, query func(context.Context) error) error {
	return waitUntilReady(ctx, func() bool { return ready() && query(ctx) == nil })
}

func waitUntilReady(ctx context.Context, ready func() bool) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for !ready() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	return nil
}

func (d *rhizaSQL) Migrate(ctx context.Context, migrations []rhiza.Migration) error {
	return retryRhizaStartup(ctx, func(attemptCtx context.Context) error {
		return d.db.Migrate(attemptCtx, migrations)
	})
}

func (d *rhizaSQL) execRaw(ctx context.Context, request rhiza.ExecuteRequest) (rhiza.ExecuteResponse, error) {
	response, err := d.db.Execute(ctx, request)
	return committedResponse(response, err)
}

func retryRhizaStartup(ctx context.Context, operation func(context.Context) error) error {
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := operation(attemptCtx)
		cancel()
		if err == nil {
			return nil
		}
		if errors.Is(err, rhiza.ErrCommitUnknown) || errors.Is(err, rhiza.ErrDurabilityUnavailable) ||
			(!errors.Is(err, rhiza.ErrNotReady) && !errors.Is(err, rhiza.ErrQuorumUnavailable)) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (d *rhizaSQL) ExecContext(ctx context.Context, statement string, args ...any) (rhiza.ExecuteResponse, error) {
	return d.execute(ctx, rhiza.ExecuteRequest{RequestID: uuid.NewString(), SQL: statement, Args: normalizeArgs(args)}, 1)
}

func (d *rhizaSQL) ExecTransaction(ctx context.Context, statements ...rhiza.SQLStatement) error {
	_, err := d.ExecTransactionResult(ctx, statements...)
	return err
}

func (d *rhizaSQL) ExecTransactionResult(ctx context.Context, statements ...rhiza.SQLStatement) (rhiza.ExecuteResponse, error) {
	if len(statements) == 0 {
		return rhiza.ExecuteResponse{}, fmt.Errorf("transaction requires at least one statement")
	}
	for i := range statements {
		statements[i].Args = normalizeArgs(statements[i].Args)
	}
	return d.execute(ctx, rhiza.ExecuteRequest{RequestID: uuid.NewString(), Statements: statements}, len(statements))
}

func committedResponse(response rhiza.ExecuteResponse, err error) (rhiza.ExecuteResponse, error) {
	if err == nil && response.Status != rhiza.MutationCommitted {
		err = fmt.Errorf("SQL mutation was rejected: %s", response.ErrorCode)
	}
	return response, err
}

func (d *rhizaSQL) QueryContext(ctx context.Context, statement string, args ...any) (*rhizaRows, error) {
	if d.trust == nil {
		return d.queryRaw(ctx, statement, args...)
	}
	// The query is intentionally between the two external reads: returning a
	// result after a restore or anchor change would otherwise leak stale state.
	first, rv, err := d.trust.get(ctx)
	if err != nil {
		return nil, err
	}
	if first.PendingEpoch != nil {
		return nil, fmt.Errorf("trust anchor has unresolved pending write")
	}
	rows, err := d.queryRaw(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	if err := d.verifyDBTrust(ctx, first.Epoch, first.Token); err != nil {
		return nil, err
	}
	second, rv2, err := d.trust.get(ctx)
	if err != nil || rv != rv2 || second.PendingEpoch != nil || second.Epoch != first.Epoch || second.Token != first.Token {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("trust anchor changed during read")
	}
	return rows, nil
}

func (d *rhizaSQL) queryRaw(ctx context.Context, statement string, args ...any) (*rhizaRows, error) {
	response, err := d.db.Query(ctx, rhiza.QueryRequest{SQL: statement, Args: normalizeArgs(args), Consistency: rhiza.ConsistencyLinearizable})
	if err != nil {
		return nil, err
	}
	return &rhizaRows{rows: response.Rows, index: -1}, nil
}

// enableTrust is called only after startup has established the durable pair.
func (d *rhizaSQL) enableTrust(anchor *trustAnchor) { d.trust = anchor }

func (d *rhizaSQL) execute(ctx context.Context, request rhiza.ExecuteRequest, originalStatements int) (rhiza.ExecuteResponse, error) {
	if d.trust == nil {
		response, err := d.db.Execute(ctx, request)
		return committedResponse(response, err)
	}
	d.trustMu.Lock()
	defer d.trustMu.Unlock()
	current, rv, err := d.trust.get(ctx)
	if err != nil {
		return rhiza.ExecuteResponse{}, err
	}
	if current.PendingEpoch != nil {
		return rhiza.ExecuteResponse{}, fmt.Errorf("trust anchor has unresolved pending write")
	}
	if err := d.verifyDBTrust(ctx, current.Epoch, current.Token); err != nil {
		return rhiza.ExecuteResponse{}, err
	}
	// Validate the immutable original request before exposing a pending state.
	request.RequestID = uuid.NewString()
	if request.SQL != "" {
		request.Statements = []rhiza.SQLStatement{{SQL: request.SQL, Args: request.Args, WantRows: request.WantRows}}
		request.SQL = ""
		request.Args = nil
	}
	if originalStatements == 0 {
		originalStatements = len(request.Statements)
	}
	returnRows := request.WantRows
	for _, statement := range request.Statements {
		returnRows = returnRows || statement.WantRows
	}
	nextEpoch := current.Epoch + 1
	nextToken := request.RequestID
	one := int64(1)
	// Request statement results solely so the wrapper can remove this tail from
	// the public receipt; callers retain the pre-fence projection below.
	request.Statements = append(request.Statements, rhiza.SQLStatement{SQL: `UPDATE trust_state SET epoch=?, token=? WHERE id=1 AND epoch=? AND token=? RETURNING last_insert_rowid()`, Args: []any{nextEpoch, nextToken, current.Epoch, current.Token}, WantRows: true, ExpectedReturnedRows: &one})
	if err := rhiza.ValidateExecuteRequest(request); err != nil {
		return rhiza.ExecuteResponse{}, fmt.Errorf("validate fenced write: %w", err)
	}
	pending := current
	pending.PendingEpoch = &nextEpoch
	pending.PendingID = request.RequestID
	pendingRV, err := d.trust.cas(ctx, rv, pending)
	if err != nil {
		return rhiza.ExecuteResponse{}, err
	}
	response, err := d.db.Execute(ctx, request)
	if err == nil && response.Status == rhiza.MutationCommitted {
		final := trustAnchorRecord{Format: current.Format, ClusterID: current.ClusterID, StorageID: current.StorageID, Epoch: nextEpoch, Token: nextToken}
		if _, casErr := d.trust.cas(ctx, pendingRV, final); casErr != nil {
			return rhiza.ExecuteResponse{}, fmt.Errorf("finalize trust anchor after committed write: %w", casErr)
		}
		if len(response.Statements) != originalStatements+1 {
			return rhiza.ExecuteResponse{}, fmt.Errorf("trust write result projection is unavailable")
		}
		response.RowsAffected = 0
		for _, statement := range response.Statements[:originalStatements] {
			response.RowsAffected += statement.RowsAffected
		}
		response.LastInsertID = response.Statements[originalStatements-1].LastInsertID
		if returnRows {
			response.Statements = response.Statements[:originalStatements]
		} else {
			response.Statements = nil
		}
		return response, nil
	}
	if err == nil {
		err = fmt.Errorf("SQL mutation was rejected: %s", response.ErrorCode)
	}
	// Only a known rejected mutation can safely clear the pending fence.
	if response.Status == rhiza.MutationRejected && d.verifyDBTrust(ctx, current.Epoch, current.Token) == nil {
		final := current
		final.PendingEpoch = nil
		final.PendingID = ""
		if _, casErr := d.trust.cas(ctx, pendingRV, final); casErr != nil {
			return rhiza.ExecuteResponse{}, fmt.Errorf("clear rejected trust write: %w", casErr)
		}
	}
	return response, err
}

func (d *rhizaSQL) verifyDBTrust(ctx context.Context, epoch int64, token string) error {
	rows, err := d.queryRaw(ctx, `SELECT epoch, token FROM trust_state WHERE id=1`)
	if err != nil {
		return fmt.Errorf("read database trust state: %w", err)
	}
	if !rows.Next() {
		return fmt.Errorf("missing database trust state")
	}
	var actualEpoch int64
	var actualToken string
	if err := rows.Scan(&actualEpoch, &actualToken); err != nil {
		return err
	}
	if rows.Next() || actualEpoch != epoch || actualToken != token {
		return fmt.Errorf("database trust state does not match trust anchor")
	}
	return nil
}

func normalizeArgs(args []any) []any {
	normalized := make([]any, len(args))
	for i, arg := range args {
		value := reflect.ValueOf(arg)
		for value.IsValid() && value.Kind() == reflect.Pointer {
			if value.IsNil() {
				value = reflect.Value{}
				break
			}
			value = value.Elem()
		}
		if value.IsValid() {
			normalized[i] = value.Interface()
		}
	}
	return normalized
}

func (d *rhizaSQL) QueryRowContext(ctx context.Context, statement string, args ...any) *rhizaRow {
	rows, err := d.QueryContext(ctx, statement, args...)
	if err != nil {
		return &rhizaRow{err: err}
	}
	if len(rows.rows) == 0 {
		return &rhizaRow{err: sql.ErrNoRows}
	}
	return &rhizaRow{row: rows.rows[0]}
}

func (r *rhizaRows) Next() bool {
	if r.err != nil || r.index+1 >= len(r.rows) {
		return false
	}
	r.index++
	return true
}

func (r *rhizaRows) Scan(dest ...any) error {
	if r.index < 0 || r.index >= len(r.rows) {
		return fmt.Errorf("scan called without a current row")
	}
	return scanValues(r.rows[r.index], dest)
}

func (r *rhizaRows) Close() error { return nil }
func (r *rhizaRows) Err() error   { return r.err }

func (r *rhizaRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	return scanValues(r.row, dest)
}

func scanValues(values []any, destinations []any) error {
	if len(values) != len(destinations) {
		return fmt.Errorf("scan destination count %d does not match column count %d", len(destinations), len(values))
	}
	for i := range values {
		if err := assignValue(destinations[i], values[i]); err != nil {
			return fmt.Errorf("scan column %d: %w", i, err)
		}
	}
	return nil
}

func assignValue(destination, value any) error {
	if scanner, ok := destination.(sql.Scanner); ok {
		return scanner.Scan(value)
	}
	dest := reflect.ValueOf(destination)
	if dest.Kind() != reflect.Pointer || dest.IsNil() {
		return fmt.Errorf("destination must be a non-nil pointer")
	}
	return assignReflect(dest.Elem(), value)
}

func assignReflect(dest reflect.Value, value any) error {
	if value == nil {
		dest.Set(reflect.Zero(dest.Type()))
		return nil
	}
	if dest.Kind() == reflect.Pointer {
		dest.Set(reflect.New(dest.Type().Elem()))
		return assignReflect(dest.Elem(), value)
	}
	source := reflect.ValueOf(value)
	if source.Type().AssignableTo(dest.Type()) {
		dest.Set(source)
		return nil
	}
	if source.Type().ConvertibleTo(dest.Type()) && source.Kind() != reflect.String {
		dest.Set(source.Convert(dest.Type()))
		return nil
	}
	switch dest.Kind() {
	case reflect.String:
		if bytes, ok := value.([]byte); ok {
			dest.SetString(string(bytes))
		} else {
			dest.SetString(fmt.Sprint(value))
		}
		return nil
	case reflect.Bool:
		text := fmt.Sprint(value)
		if number, err := strconv.ParseInt(text, 10, 64); err == nil {
			dest.SetBool(number != 0)
			return nil
		}
		parsed, err := strconv.ParseBool(text)
		if err != nil {
			return err
		}
		dest.SetBool(parsed)
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		parsed, err := strconv.ParseInt(fmt.Sprint(value), 10, dest.Type().Bits())
		if err != nil {
			return err
		}
		dest.SetInt(parsed)
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		parsed, err := strconv.ParseUint(fmt.Sprint(value), 10, dest.Type().Bits())
		if err != nil {
			return err
		}
		dest.SetUint(parsed)
		return nil
	}
	return fmt.Errorf("cannot assign %T to %s", value, dest.Type())
}

func rhizaConfigFromEnv(dataDir string) (rhiza.Config, error) {
	adminToken := os.Getenv("TERNAL_DATA_ADMIN_TOKEN")
	if os.Getenv("TERNAL_REQUIRE_DATA_ADMIN_TOKEN") == "1" && len(adminToken) < 32 {
		return rhiza.Config{}, fmt.Errorf("TERNAL_DATA_ADMIN_TOKEN must be at least 32 bytes")
	}
	var members []rhiza.Member
	if raw := os.Getenv("TERNAL_DATA_CLUSTER_MEMBERS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &members); err != nil {
			return rhiza.Config{}, fmt.Errorf("parse TERNAL_DATA_CLUSTER_MEMBERS: %w", err)
		}
	}
	multiNode := os.Getenv("TERNAL_DATA_MULTI_NODE")
	expectedMemberIDs := os.Getenv("TERNAL_DATA_EXPECTED_MEMBER_IDS")
	if multiNode != "" && multiNode != "0" && multiNode != "1" {
		return rhiza.Config{}, fmt.Errorf("TERNAL_DATA_MULTI_NODE must be 0 or 1")
	}
	if multiNode == "1" {
		if len(members) != 3 {
			return rhiza.Config{}, fmt.Errorf("multi-node mode requires exactly three TERNAL_DATA_CLUSTER_MEMBERS")
		}
		if len(adminToken) < 32 {
			return rhiza.Config{}, fmt.Errorf("multi-node mode requires TERNAL_DATA_ADMIN_TOKEN with at least 32 bytes")
		}
		seenIDs := make(map[string]struct{}, len(members))
		seenTokens := make(map[string]struct{}, len(members))
		for _, member := range members {
			id := string(member.ID)
			if _, duplicate := seenIDs[id]; duplicate {
				return rhiza.Config{}, fmt.Errorf("multi-node member IDs must be distinct")
			}
			seenIDs[id] = struct{}{}
			if len(member.Token) < 32 {
				return rhiza.Config{}, fmt.Errorf("multi-node member %s token must contain at least 32 bytes", member.ID)
			}
			if member.Token == adminToken {
				return rhiza.Config{}, fmt.Errorf("multi-node member %s token must differ from TERNAL_DATA_ADMIN_TOKEN", member.ID)
			}
			if _, duplicate := seenTokens[member.Token]; duplicate {
				return rhiza.Config{}, fmt.Errorf("multi-node member tokens must be distinct")
			}
			seenTokens[member.Token] = struct{}{}
		}
		if expectedMemberIDs != "" {
			expected := strings.Split(expectedMemberIDs, ",")
			if len(expected) != 3 {
				return rhiza.Config{}, fmt.Errorf("TERNAL_DATA_EXPECTED_MEMBER_IDS must contain exactly three IDs")
			}
			expectedSeen := make(map[string]struct{}, len(expected))
			for _, id := range expected {
				if id == "" {
					return rhiza.Config{}, fmt.Errorf("TERNAL_DATA_EXPECTED_MEMBER_IDS must not contain an empty ID")
				}
				if _, duplicate := expectedSeen[id]; duplicate {
					return rhiza.Config{}, fmt.Errorf("TERNAL_DATA_EXPECTED_MEMBER_IDS must contain distinct IDs")
				}
				expectedSeen[id] = struct{}{}
				if _, found := seenIDs[id]; !found {
					return rhiza.Config{}, fmt.Errorf("multi-node member set is missing expected ID %s", id)
				}
			}
		}
	} else if len(members) != 0 || expectedMemberIDs != "" {
		return rhiza.Config{}, fmt.Errorf("HA member configuration requires TERNAL_DATA_MULTI_NODE=1")
	}
	syncInterval, err := durationEnv("TERNAL_OBJECT_STORE_SYNC_INTERVAL", time.Minute)
	if err != nil {
		return rhiza.Config{}, err
	}
	batchDelay, err := durationEnv("TERNAL_OBJECT_STORE_BATCH_DELAY", 2*time.Millisecond)
	if err != nil {
		return rhiza.Config{}, err
	}
	checkpointInterval, err := durationEnv("TERNAL_DATA_CHECKPOINT_INTERVAL", 15*time.Minute)
	if err != nil {
		return rhiza.Config{}, err
	}
	return rhiza.Config{
		ClusterID:            envOr("TERNAL_DATA_CLUSTER_ID", "ternal"),
		NodeID:               envOr("TERNAL_DATA_NODE_ID", "node-1"),
		DataDir:              dataDir,
		BindAddr:             envOr("TERNAL_DATA_BIND_ADDR", "127.0.0.1:0"),
		PeerAddr:             envOr("TERNAL_DATA_PEER_ADDR", "127.0.0.1:0"),
		AdminToken:           adminToken,
		Members:              members,
		ObjStoreEndpoint:     os.Getenv("TERNAL_OBJECT_STORE_ENDPOINT"),
		ObjStoreBucket:       os.Getenv("TERNAL_OBJECT_STORE_BUCKET"),
		ObjStoreProvider:     os.Getenv("TERNAL_OBJECT_STORE_PROVIDER"),
		ObjStoreDir:          os.Getenv("TERNAL_OBJECT_STORE_DIR"),
		ObjStorePrefix:       os.Getenv("TERNAL_OBJECT_STORE_PREFIX"),
		ObjStoreRegion:       os.Getenv("TERNAL_OBJECT_STORE_REGION"),
		ObjStoreInsecure:     os.Getenv("TERNAL_OBJECT_STORE_INSECURE") == "true",
		ObjStoreAccessKey:    os.Getenv("TERNAL_OBJECT_STORE_ACCESS_KEY"),
		ObjStoreSecretKey:    os.Getenv("TERNAL_OBJECT_STORE_SECRET_KEY"),
		ObjStoreSessionToken: os.Getenv("TERNAL_OBJECT_STORE_SESSION_TOKEN"),
		ObjStoreDurability:   rhiza.ObjectStoreDurability(envOr("TERNAL_OBJECT_STORE_DURABILITY", string(rhiza.ObjectStoreDurabilityAsync))),
		ObjStoreSyncInterval: syncInterval,
		ObjStoreBatchDelay:   batchDelay,
		CheckpointInterval:   checkpointInterval,
	}, nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("%s must be positive", key)
	}
	return duration, nil
}
