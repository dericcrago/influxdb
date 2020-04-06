package kv

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/influxdata/influxdb/v2"
	"github.com/influxdata/influxdb/v2/kit/tracing"
)

// IndexStore provides a entity store that uses an index lookup.
// The index store manages deleting and creating indexes for the
// caller. The index is automatically used if the FindEnt entity
// entity does not have the primary key.
type IndexStore struct {
	Resource   string
	EntStore   *StoreBase
	IndexStore *StoreBase
}

// Init creates the entity and index buckets.
func (s *IndexStore) Init(ctx context.Context, tx Tx) error {
	span, ctx := tracing.StartSpanFromContext(ctx)
	defer span.Finish()

	initFns := []func(context.Context, Tx) error{
		s.EntStore.Init,
		s.IndexStore.Init,
	}
	for _, fn := range initFns {
		if err := fn(ctx, tx); err != nil {
			return err
		}
	}
	return nil
}

// Delete deletes entities and associated indexes.
func (s *IndexStore) Delete(ctx context.Context, tx Tx, opts DeleteOpts) error {
	span, ctx := tracing.StartSpanFromContext(ctx)
	defer span.Finish()

	deleteIndexedRelationFn := func(k []byte, v interface{}) error {
		ent, err := s.EntStore.ConvertValToEntFn(k, v)
		if err != nil {
			return err
		}
		return s.IndexStore.DeleteEnt(ctx, tx, ent)
	}
	opts.DeleteRelationFns = append(opts.DeleteRelationFns, deleteIndexedRelationFn)
	return s.EntStore.Delete(ctx, tx, opts)
}

// DeleteEnt deletes an entity and associated index.
func (s *IndexStore) DeleteEnt(ctx context.Context, tx Tx, ent Entity) error {
	span, ctx := tracing.StartSpanFromContext(ctx)
	defer span.Finish()

	existing, err := s.FindEnt(ctx, tx, ent)
	if err != nil {
		return err
	}

	if err := s.EntStore.DeleteEnt(ctx, tx, ent); err != nil {
		return err
	}

	decodedEnt, err := s.EntStore.ConvertValToEntFn(nil, existing)
	if err != nil {
		return err
	}

	return s.IndexStore.DeleteEnt(ctx, tx, decodedEnt)
}

// Find provides a mechanism for looking through the bucket via
// the set options. When a prefix is provided, it will be used within
// the entity store. If you would like to search the index store, then
// you can by calling the index store directly.
func (s *IndexStore) Find(ctx context.Context, tx Tx, opts FindOpts) error {
	span, ctx := tracing.StartSpanFromContext(ctx)
	defer span.Finish()

	return s.EntStore.Find(ctx, tx, opts)
}

// FindEnt returns the decoded entity body via teh provided entity.
// An example entity should not include a Body, but rather the ID,
// Name, or OrgID. If no ID is provided, then the algorithm assumes
// you are looking up the entity by the index.
func (s *IndexStore) FindEnt(ctx context.Context, tx Tx, ent Entity) (interface{}, error) {
	span, ctx := tracing.StartSpanFromContext(ctx)
	defer span.Finish()

	_, err := s.EntStore.EntKey(ctx, ent)
	if err != nil {
		if _, idxErr := s.IndexStore.EntKey(ctx, ent); idxErr != nil {
			return nil, &influxdb.Error{
				Code: influxdb.EInvalid,
				Msg:  "no key was provided for " + s.Resource,
			}
		}
	}
	if err != nil {
		return s.findByIndex(ctx, tx, ent)
	}
	return s.EntStore.FindEnt(ctx, tx, ent)
}

func (s *IndexStore) findByIndex(ctx context.Context, tx Tx, ent Entity) (interface{}, error) {
	span, ctx := tracing.StartSpanFromContext(ctx)
	defer span.Finish()

	idxEncodedID, err := s.IndexStore.FindEnt(ctx, tx, ent)
	if err != nil {
		return nil, err
	}

	indexKey, err := s.IndexStore.EntKey(ctx, ent)
	if err != nil {
		return nil, err
	}

	indexEnt, err := s.IndexStore.ConvertValToEntFn(indexKey, idxEncodedID)
	if err != nil {
		return nil, err
	}

	return s.EntStore.FindEnt(ctx, tx, indexEnt)
}

// Put will persist the entity into both the entity store and the index store.
func (s *IndexStore) Put(ctx context.Context, tx Tx, ent Entity, opts ...PutOptionFn) error {
	span, ctx := tracing.StartSpanFromContext(ctx)
	defer span.Finish()

	var opt putOption
	for _, o := range opts {
		if err := o(&opt); err != nil {
			return &influxdb.Error{
				Code: influxdb.EConflict,
				Err:  err,
			}
		}
	}

	if err := s.putValidate(ctx, tx, ent, opt); err != nil {
		return err
	}

	if err := s.IndexStore.Put(ctx, tx, ent); err != nil {
		return err
	}

	return s.EntStore.Put(ctx, tx, ent)
}

func (s *IndexStore) putValidate(ctx context.Context, tx Tx, ent Entity, opt putOption) error {
	if opt.isNew {
		return s.validNew(ctx, tx, ent)
	}
	if opt.isUpdate {
		return s.validUpdate(ctx, tx, ent)
	}
	return nil
}

func (s *IndexStore) validNew(ctx context.Context, tx Tx, ent Entity) error {
	_, err := s.IndexStore.FindEnt(ctx, tx, ent)
	if err == nil || influxdb.ErrorCode(err) != influxdb.ENotFound {
		key, _ := s.IndexStore.EntKey(ctx, ent)
		return &influxdb.Error{
			Code: influxdb.EConflict,
			Msg:  fmt.Sprintf("%s is not unique for key %s", s.Resource, string(key)),
			Err:  err,
		}
	}

	if _, err := s.EntStore.FindEnt(ctx, tx, ent); err != nil && influxdb.ErrorCode(err) != influxdb.ENotFound {
		return &influxdb.Error{Code: influxdb.EInternal, Err: err}
	}
	return nil
}

func (s *IndexStore) validUpdate(ctx context.Context, tx Tx, ent Entity) error {
	// first check to make sure the existing entity exists in the ent store
	_, err := s.EntStore.FindEnt(ctx, tx, Entity{PK: ent.PK})
	if err != nil {
		return err
	}

	idxVal, err := s.IndexStore.FindEnt(ctx, tx, ent)
	if err != nil {
		if influxdb.ErrorCode(err) == influxdb.ENotFound {
			return nil
		}
		return err
	}

	idxKey, err := s.IndexStore.EntKey(ctx, ent)
	if err != nil {
		return err
	}

	indexEnt, err := s.IndexStore.ConvertValToEntFn(idxKey, idxVal)
	if err != nil {
		return err
	}

	if err := sameKeys(ent.PK, indexEnt.PK); err != nil {
		if _, err := s.EntStore.FindEnt(ctx, tx, ent); influxdb.ErrorCode(err) == influxdb.ENotFound {
			key, _ := ent.PK()
			return &influxdb.Error{
				Code: influxdb.ENotFound,
				Msg:  fmt.Sprintf("%s does not exist for key %s", s.Resource, string(key)),
				Err:  err,
			}
		}
		key, _ := indexEnt.UniqueKey()
		return &influxdb.Error{
			Code: influxdb.EConflict,
			Msg:  fmt.Sprintf("%s entity update conflicts with an existing entity for key %s", s.Resource, string(key)),
		}
	}

	return s.IndexStore.DeleteEnt(ctx, tx, ent)
}

func sameKeys(key1, key2 EncodeFn) error {
	pk1, err := key1()
	if err != nil {
		return err
	}
	pk2, err := key2()
	if err != nil {
		return err
	}

	if !bytes.Equal(pk1, pk2) {
		return errors.New("keys differ")
	}
	return nil
}
