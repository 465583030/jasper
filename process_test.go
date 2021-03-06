package jasper

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type processConstructor func(context.Context, *CreateOptions) (Process, error)

func makeLockingProcess(pmake processConstructor) processConstructor {
	return func(ctx context.Context, opts *CreateOptions) (Process, error) {
		proc, err := pmake(ctx, opts)
		if err != nil {
			return nil, err
		}
		return &localProcess{proc: proc}, nil
	}
}

func TestProcessImplementations(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	httpClient := &http.Client{}

	for cname, makeProc := range map[string]processConstructor{
		"BlockingNoLock":   newBlockingProcess,
		"BlockingWithLock": makeLockingProcess(newBlockingProcess),
		"BasicNoLock":      newBasicProcess,
		"BasicWithLock":    makeLockingProcess(newBasicProcess),
		"REST": func(ctx context.Context, opts *CreateOptions) (Process, error) {

			srv, port := makeAndStartService(ctx, httpClient)
			if port < 100 || srv == nil {
				return nil, errors.New("fixture creation failure")
			}

			client := &restClient{
				prefix: fmt.Sprintf("http://localhost:%d/jasper/v1", port),
				client: httpClient,
			}

			return client.Create(ctx, opts)
		},
	} {
		t.Run(cname, func(t *testing.T) {
			for name, testCase := range map[string]func(context.Context, *testing.T, *CreateOptions, processConstructor){
				"WithPopulatedArgsCommandCreationPasses": func(ctx context.Context, t *testing.T, opts *CreateOptions, makep processConstructor) {
					assert.NotZero(t, opts.Args)
					proc, err := makep(ctx, opts)
					require.NoError(t, err)
					assert.NotNil(t, proc)
				},
				"ErrorToCreateWithInvalidArgs": func(ctx context.Context, t *testing.T, opts *CreateOptions, makep processConstructor) {
					opts.Args = []string{}
					proc, err := makep(ctx, opts)
					assert.Error(t, err)
					assert.Nil(t, proc)
				},
				"WithCancledContextProcessCreationFailes": func(ctx context.Context, t *testing.T, opts *CreateOptions, makep processConstructor) {
					pctx, pcancel := context.WithCancel(ctx)
					pcancel()
					proc, err := makep(pctx, opts)
					assert.Error(t, err)
					assert.Nil(t, proc)
				},
				"CanceledContextTimesOutEarly": func(ctx context.Context, t *testing.T, opts *CreateOptions, makep processConstructor) {
					pctx, pcancel := context.WithTimeout(ctx, 200*time.Millisecond)
					defer pcancel()
					startAt := time.Now()
					opts.Args = []string{"sleep", "101"}
					proc, err := makep(pctx, opts)
					assert.NoError(t, err)

					time.Sleep(100 * time.Millisecond) // let time pass...
					require.NotNil(t, proc)
					assert.False(t, proc.Info(ctx).Successful)
					assert.True(t, time.Since(startAt) < 400*time.Millisecond)
				},
				"ProcessLacksTagsByDefault": func(ctx context.Context, t *testing.T, opts *CreateOptions, makep processConstructor) {
					proc, err := makep(ctx, opts)
					require.NoError(t, err)
					tags := proc.GetTags()
					assert.Empty(t, tags)
				},
				"ProcessTagsPersist": func(ctx context.Context, t *testing.T, opts *CreateOptions, makep processConstructor) {
					opts.Tags = []string{"foo"}
					proc, err := makep(ctx, opts)
					require.NoError(t, err)
					tags := proc.GetTags()
					assert.Contains(t, tags, "foo")
				},
				"InfoHasMatchingID": func(ctx context.Context, t *testing.T, opts *CreateOptions, makep processConstructor) {
					proc, err := makep(ctx, opts)
					if assert.NoError(t, err) {
						assert.Equal(t, proc.ID(), proc.Info(ctx).ID)
					}
				},
				"ResetTags": func(ctx context.Context, t *testing.T, opts *CreateOptions, makep processConstructor) {
					proc, err := makep(ctx, opts)
					require.NoError(t, err)
					proc.Tag("foo")
					assert.Contains(t, proc.GetTags(), "foo")
					proc.ResetTags()
					assert.Len(t, proc.GetTags(), 0)
				},
				"TagsAreSetLike": func(ctx context.Context, t *testing.T, opts *CreateOptions, makep processConstructor) {
					proc, err := makep(ctx, opts)
					require.NoError(t, err)

					for i := 0; i < 100; i++ {
						proc.Tag("foo")
					}

					assert.Len(t, proc.GetTags(), 1)
					proc.Tag("bar")
					assert.Len(t, proc.GetTags(), 2)
				},
				"CompleteIsTrueAfterWait": func(ctx context.Context, t *testing.T, opts *CreateOptions, makep processConstructor) {
					proc, err := makep(ctx, opts)
					require.NoError(t, err)
					time.Sleep(10 * time.Millisecond) // give the process time to start background machinery
					assert.NoError(t, proc.Wait(ctx))
					assert.True(t, proc.Complete(ctx))
				},
				"WaitReturnsWithCancledContext": func(ctx context.Context, t *testing.T, opts *CreateOptions, makep processConstructor) {
					opts.Args = []string{"sleep", "101"}
					pctx, pcancel := context.WithCancel(ctx)
					proc, err := makep(ctx, opts)
					require.NoError(t, err)
					assert.True(t, proc.Running(ctx))
					assert.NoError(t, err)
					pcancel()
					assert.Error(t, proc.Wait(pctx))
				},
				"RegisterTriggerErrorsForNil": func(ctx context.Context, t *testing.T, opts *CreateOptions, makep processConstructor) {
					proc, err := makep(ctx, opts)
					require.NoError(t, err)
					assert.Error(t, proc.RegisterTrigger(ctx, nil))
				},
				"DefaultTriggerSucceeds": func(ctx context.Context, t *testing.T, opts *CreateOptions, makep processConstructor) {
					if cname == "REST" {
						t.Skip("remote triggers are not supported on rest processes")
					}
					proc, err := makep(ctx, opts)
					assert.NoError(t, err)
					assert.NoError(t, proc.RegisterTrigger(ctx, makeDefaultTrigger(ctx, nil, opts, "foo")))
				},
				// "": func(ctx context.Context, t *testing.T, opts *CreateOptions, makep processConstructor) {},
			} {
				t.Run(name, func(t *testing.T) {
					tctx, cancel := context.WithTimeout(ctx, taskTimeout)
					defer cancel()

					opts := &CreateOptions{Args: []string{"ls"}}
					testCase(tctx, t, opts, makeProc)
				})
			}
		})
	}
}
