package pdscrape

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/cresta/zapctx/testhelp/testhelp"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestPdScrape(t *testing.T) {
	if os.Getenv("PAGERDUTY_TOKEN") == "" {
		t.Skipf("Skipping test because pd token not set in env PAGERDUTY_TOKEN")
	}
	x := PdScrape{
		Log: testhelp.ZapTestingLogger(t),
	}
	ctx := context.Background()
	require.NoError(t, x.Init(ctx, os.Getenv("PAGERDUTY_TOKEN")))
	require.NoError(t, x.Scrape(ctx))
	av, err := x.Availabilities(ctx, time.Hour*24)
	require.NoError(t, err)
	x.Log.Warn(ctx, "avails are ", zap.Any("av", av))
	ic, err := x.IncidentCounts(ctx, time.Hour*24)
	require.NoError(t, err)
	x.Log.Warn(ctx, "counts are", zap.Any("ic", ic))
}

func TestTimeRange(t *testing.T) {
	start := time.Now()
	end := time.Now().Add(time.Hour)
	doTest := func(removeList []timeRange, expected time.Duration, expectedLen int) func(t *testing.T) {
		return func(t *testing.T) {
			tr := timeRange{
				start: start,
				end:   end,
			}
			r := rangeList{
				ranges: []timeRange{tr},
			}
			for _, toRemove := range removeList {
				r.sub(toRemove)
				t.Log(r.String())
			}
			require.InDelta(t, expected, r.totalTime(), float64(time.Second))
			require.Equal(t, expectedLen, len(r.ranges))
		}
	}
	t.Run("default", doTest(nil, time.Hour, 1))
	t.Run("fully removed", doTest([]timeRange{{
		start: start.Add(-time.Minute),
		end:   end.Add(time.Minute),
	}}, 0, 0))
	t.Run("no remove", doTest([]timeRange{{
		start: end.Add(time.Minute),
		end:   end.Add(time.Minute * 2),
	}}, time.Hour, 1))
	t.Run("no remove start", doTest([]timeRange{{
		start: start.Add(-time.Minute * 2),
		end:   start.Add(-time.Minute),
	}}, time.Hour, 1))
	t.Run("Split", doTest([]timeRange{{
		start: start,
		end:   start.Add(time.Minute),
	}}, time.Minute*59, 2))
	t.Run("Split twice", doTest([]timeRange{
		{
			start: start.Add(time.Minute),
			end:   start.Add(time.Minute * 5),
		}, {
			start: start.Add(time.Minute * 4),
			end:   start.Add(time.Minute * 6),
		},
	}, time.Minute*55, 2))
	t.Run("Split ends", doTest([]timeRange{
		{
			start: start.Add(-time.Minute),
			end:   start.Add(time.Minute * 5),
		}, {
			start: end.Add(-time.Minute * 5),
			end:   end.Add(time.Minute),
		},
	}, time.Minute*50, 1))
}
