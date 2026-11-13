package session

import (
	"context"
	"errors"
	"testing"

	"github.com/opencode-ai/opencode/internal/db"
)

func TestDeleteHandlerGuardsRootTreeAndPreservesChildSemantics(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	root, err := svc.Create(ctx, "root")
	if err != nil {
		t.Fatal(err)
	}
	child, err := svc.CreateTaskSession(ctx, "task-child", root.ID, "child")
	if err != nil {
		t.Fatal(err)
	}
	denied := errors.New("run owned by another worker")
	var seen []DeleteRequest
	SetDeleteHandler(svc, func(ctx context.Context, req DeleteRequest, mutate func(db.Querier) error) error {
		seen = append(seen, req)
		if req.Tree {
			return denied
		}
		return mutate(svc.(*service).q)
	})
	if err = svc.Delete(ctx, child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Get(ctx, root.ID); err != nil {
		t.Fatal("single child removal deleted root")
	}
	if len(seen) != 1 || seen[0].Tree || seen[0].RootSessionID != root.ID {
		t.Fatalf("child request=%+v", seen)
	}
	if err = svc.Delete(ctx, root.ID); !errors.Is(err, denied) {
		t.Fatalf("root delete: %v", err)
	}
	if _, err = svc.Get(ctx, root.ID); err != nil {
		t.Fatal("refused deletion still removed root")
	}
	if !seen[1].Tree || seen[1].RootSessionID != root.ID {
		t.Fatalf("root request=%+v", seen[1])
	}
	SetDeleteHandler(svc, nil)
	if err = svc.DeleteTree(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
}
