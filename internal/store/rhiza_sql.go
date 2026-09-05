package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mrchypark/rhiza"
)

type rhizaSQL struct{ db *rhiza.DB }

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
	return waitUntilReady(ctx, d.db.Ready)
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
	return d.db.Migrate(ctx, migrations)
}

func (d *rhizaSQL) ExecContext(ctx context.Context, statement string, args ...any) (rhiza.ExecuteResponse, error) {
	response, err := d.db.Execute(ctx, rhiza.ExecuteRequest{RequestID: uuid.NewString(), SQL: statement, Args: normalizeArgs(args)})
	return committedResponse(response, err)
}

func (d *rhizaSQL) ExecTransaction(ctx context.Context, statements ...rhiza.SQLStatement) error {
	_, err := d.ExecTransactionResult(ctx, statements...)
	return err
}

func (d *rhizaSQL) ExecTransactionResult(ctx context.Context, statements ...rhiza.SQLStatement) (rhiza.ExecuteResponse, error) {
	for i := range statements {
		statements[i].Args = normalizeArgs(statements[i].Args)
	}
	response, err := d.db.Execute(ctx, rhiza.ExecuteRequest{RequestID: uuid.NewString(), Statements: statements})
	return committedResponse(response, err)
}

func committedResponse(response rhiza.ExecuteResponse, err error) (rhiza.ExecuteResponse, error) {
	if err == nil && response.Status != rhiza.MutationCommitted {
		err = fmt.Errorf("SQL mutation was rejected: %s", response.ErrorCode)
	}
	return response, err
}

func (d *rhizaSQL) QueryContext(ctx context.Context, statement string, args ...any) (*rhizaRows, error) {
	response, err := d.db.Query(ctx, rhiza.QueryRequest{SQL: statement, Args: normalizeArgs(args), Consistency: rhiza.ConsistencyLinearizable})
	if err != nil {
		return nil, err
	}
	return &rhizaRows{rows: response.Rows, index: -1}, nil
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
