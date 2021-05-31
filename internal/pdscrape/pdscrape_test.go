package pdscrape

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPdScrape(t *testing.T) {
	x := PdScrape{}
	ctx := context.Background()
	require.NoError(t, x.Init(ctx, ""))
	require.NoError(t, x.Scrape(ctx))
}
