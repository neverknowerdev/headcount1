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
	// Seal keeps "" empty and never double-encrypts an already-sealed value.
	return Default().Seal(s)
}
