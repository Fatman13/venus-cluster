package chain

import (
	"context"

	blocks "github.com/ipfs/go-block-format"
	"github.com/ipfs/go-cid"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	"golang.org/x/xerrors"
)

type apiBStore struct {
	api API
}

// NewAPIBlockstore create new blockstore api
func NewAPIBlockstore(a API) blockstore.Blockstore {
	return &apiBStore{
		api: a,
	}
}

// DeleteBlock implements Blockstore.DeleteBlock.
func (a *apiBStore) DeleteBlock(context.Context, cid.Cid) error {
	return xerrors.New("not supported")
}

// Has implements Blockstore.Has.
func (a *apiBStore) Has(ctx context.Context, c cid.Cid) (bool, error) {
	return a.api.ChainHasObj(ctx, c)
}

// Get implements Blockstore.Get.
func (a *apiBStore) Get(ctx context.Context, c cid.Cid) (blocks.Block, error) {
	bb, err := a.api.ChainReadObj(ctx, c)
	if err != nil {
		return nil, err
	}
	return blocks.NewBlockWithCid(bb, c)
}

// GetSize implements Blockstore.GetSize.
func (a *apiBStore) GetSize(ctx context.Context, c cid.Cid) (int, error) {
	bb, err := a.api.ChainReadObj(ctx, c)
	if err != nil {
		return 0, err
	}
	return len(bb), nil
}

// Put implements Blockstore.Put.
func (a *apiBStore) Put(context.Context, blocks.Block) error {
	return xerrors.New("not supported")
}

// PutMany implements Blockstore.PutMany.
func (a *apiBStore) PutMany(context.Context, []blocks.Block) error {
	return xerrors.New("not supported")
}

// AllKeysChan implements Blockstore.AllKeysChan.
func (a *apiBStore) AllKeysChan(_ context.Context) (<-chan cid.Cid, error) {
	return nil, xerrors.New("not supported")
}

// HashOnRead implements Blockstore.HashOnRead.
func (a *apiBStore) HashOnRead(enabled bool) {}

var _ blockstore.Blockstore = &apiBStore{}
