package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strconv"
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

func (d *rhizaSQL) ExecContext(ctx context.Context, statement string, args ...any) (rhiza.ExecuteResponse, error) {
	return d.db.Execute(ctx, rhiza.ExecuteRequest{RequestID: uuid.NewString(), SQL: statement, Args: normalizeArgs(args)})
}

func (d *rhizaSQL) ExecTransaction(ctx context.Context, statements ...rhiza.SQLStatement) error {
	for i := range statements {
		statements[i].Args = normalizeArgs(statements[i].Args)
	}
	_, err := d.db.Execute(ctx, rhiza.ExecuteRequest{RequestID: uuid.NewString(), Statements: statements})
	return err
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
	adminToken := os.Getenv("RHIZA_ADMIN_TOKEN")
	if os.Getenv("TERNAL_REQUIRE_RHIZA_ADMIN_TOKEN") == "1" && len(adminToken) < 32 {
		return rhiza.Config{}, fmt.Errorf("RHIZA_ADMIN_TOKEN must be at least 32 bytes")
	}
	var members []rhiza.Member
	if raw := os.Getenv("RHIZA_CLUSTER_MEMBERS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &members); err != nil {
			return rhiza.Config{}, fmt.Errorf("parse RHIZA_CLUSTER_MEMBERS: %w", err)
		}
	}
	if os.Getenv("TERNAL_RHIZA_MULTI_NODE") == "1" && len(members) < 3 {
		return rhiza.Config{}, fmt.Errorf("multi-node mode requires at least three RHIZA_CLUSTER_MEMBERS")
	}
	syncInterval, err := durationEnv("RHIZA_OBJSTORE_SYNC_INTERVAL", time.Minute)
	if err != nil {
		return rhiza.Config{}, err
	}
	batchDelay, err := durationEnv("RHIZA_OBJSTORE_BATCH_DELAY", 2*time.Millisecond)
	if err != nil {
		return rhiza.Config{}, err
	}
	return rhiza.Config{
		ClusterID:            envOr("RHIZA_CLUSTER_ID", envOr("TERNAL_RHIZA_CLUSTER_ID", "ternal")),
		NodeID:               envOr("RHIZA_NODE_ID", "node-1"),
		DataDir:              dataDir,
		BindAddr:             envOr("RHIZA_BIND_ADDR", "127.0.0.1:0"),
		PeerAddr:             envOr("RHIZA_PEER_ADDR", "127.0.0.1:0"),
		AdminToken:           adminToken,
		Members:              members,
		ObjStoreEndpoint:     os.Getenv("RHIZA_OBJSTORE_ENDPOINT"),
		ObjStoreBucket:       os.Getenv("RHIZA_OBJSTORE_BUCKET"),
		ObjStoreProvider:     os.Getenv("RHIZA_OBJSTORE_PROVIDER"),
		ObjStoreDir:          os.Getenv("RHIZA_OBJSTORE_DIR"),
		ObjStorePrefix:       os.Getenv("RHIZA_OBJSTORE_PREFIX"),
		ObjStoreRegion:       os.Getenv("RHIZA_OBJSTORE_REGION"),
		ObjStoreInsecure:     os.Getenv("RHIZA_OBJSTORE_INSECURE") == "true",
		ObjStoreAccessKey:    os.Getenv("RHIZA_OBJSTORE_ACCESS_KEY"),
		ObjStoreSecretKey:    os.Getenv("RHIZA_OBJSTORE_SECRET_KEY"),
		ObjStoreSessionToken: os.Getenv("RHIZA_OBJSTORE_SESSION_TOKEN"),
		ObjStoreDurability:   rhiza.ObjectStoreDurability(envOr("RHIZA_OBJSTORE_DURABILITY", string(rhiza.ObjectStoreDurabilityAsync))),
		ObjStoreSyncInterval: syncInterval,
		ObjStoreBatchDelay:   batchDelay,
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
