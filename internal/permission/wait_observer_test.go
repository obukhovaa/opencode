package permission

import (
	"context"
	"testing"
)

type testWaitObserver struct{ starts, ends int }

func (o *testWaitObserver) WaitStarted() { o.starts++ }
func (o *testWaitObserver) WaitEnded()   { o.ends++ }

func TestWithWaitObserverChainsEveryLevel(t *testing.T) {
	first, second, third := &testWaitObserver{}, &testWaitObserver{}, &testWaitObserver{}
	ctx := WithWaitObserver(WithWaitObserver(WithWaitObserver(context.Background(), first), second), third)
	ctx = WithWaitObserver(ctx, nil)
	observer := ctx.Value(waitObserverKeyType{}).(WaitObserver)
	observer.WaitStarted()
	observer.WaitStarted()
	observer.WaitEnded()
	observer.WaitEnded()
	for _, got := range []*testWaitObserver{first, second, third} {
		if got.starts != 2 || got.ends != 2 {
			t.Fatalf("observer lost nested wait: %+v", got)
		}
	}
}
