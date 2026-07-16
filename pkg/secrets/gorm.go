package secrets

import (
	"context"
	"fmt"
	"reflect"

	"gorm.io/gorm/schema"
)

// GormSerializer encrypts string fields tagged `gorm:"serializer:secret"`
// transparently: sealed on every write, opened on every read. The rest of
// the codebase keeps working with plaintext in memory, and raw secrets never
// reach the database.
//
// Registered by the db package's init so every gorm.Open in the codebase
// (including tests) gets it.
type GormSerializer struct{}

var _ schema.SerializerInterface = GormSerializer{}

func (GormSerializer) Scan(ctx context.Context, field *schema.Field, dst reflect.Value, dbValue interface{}) error {
	var stored string
	switch v := dbValue.(type) {
	case nil:
	case string:
		stored = v
	case []byte:
		stored = string(v)
	default:
		return fmt.Errorf("secrets: field %s has unsupported db type %T", field.Name, dbValue)
	}
	// Open passes non-sealed values through, so rows written before
	// encryption was introduced keep loading; they are re-sealed on the next
	// write (see db.EncryptPlaintextSecrets for the startup sweep).
	plain, err := Default().Open(stored)
	if err != nil {
		return err
	}
	field.ReflectValueOf(ctx, dst).SetString(plain)
	return nil
}

func (GormSerializer) Value(ctx context.Context, field *schema.Field, dst reflect.Value, fieldValue interface{}) (interface{}, error) {
	s, ok := fieldValue.(string)
	if !ok {
		return nil, fmt.Errorf("secrets: field %s must be a string, got %T", field.Name, fieldValue)
	}
	// Rows owned by a user are sealed with that user's DEK; ownerless rows
	// (builtins, legacy fields) with the root DEK. Reading the sibling
	// UserID here is safe: writes go through full structs (Create/Save).
	// Seal/SealForUser keep "" empty and never double-encrypt.
	if uid := ownerUserID(ctx, field, dst); uid != 0 {
		return Default().SealForUser(uid, s)
	}
	return Default().Seal(s)
}

// ownerUserID returns the value of the model's UserID field, or 0 when the
// model has no such field or it is nil/zero.
func ownerUserID(ctx context.Context, field *schema.Field, dst reflect.Value) int32 {
	if field.Schema == nil {
		return 0
	}
	owner := field.Schema.LookUpField("UserID")
	if owner == nil {
		return 0
	}
	v, isZero := owner.ValueOf(ctx, dst)
	if isZero || v == nil {
		return 0
	}
	switch id := v.(type) {
	case int32:
		return id
	case *int32:
		if id != nil {
			return *id
		}
	case int64:
		return int32(id)
	case int:
		return int32(id)
	}
	return 0
}
