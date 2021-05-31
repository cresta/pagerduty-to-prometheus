package pdscrape

import (
	"context"
	"errors"
	"fmt"
	"github.com/PagerDuty/go-pagerduty"
	"github.com/cresta/zapctx"
	"go.uber.org/zap"
	"os"
)

type PdScrape struct {
	Client *pagerduty.Client
	Log *zapctx.Logger
}

func (p *PdScrape) Init(ctx context.Context, token string) error {
	if token == "" {
		token = os.Getenv("PAGERDUTY_TOKEN")
	}
	if token == "" {
		return errors.New("please set pagerduty token")
	}
	p.Client = pagerduty.NewClient(token)
	u, err := p.Client.GetCurrentUserWithContext(ctx, pagerduty.GetCurrentUserOptions{})
	if err != nil {
		return fmt.Errorf("unable to validate token with current user: %w", err)
	}
	p.Log.Info(ctx, "fetched current user", zap.String("user", u.Name))
	return nil
}

func (p *PdScrape) Scrape(ctx context.Context) error {
	p.Log.Info(ctx, "Listing services")
	services, err := p.Client.ListServicesPaginated(ctx, pagerduty.ListServiceOptions{})
	if err != nil {
		return fmt.Errorf("unable to list services: %w", err)
	}
	for _, s := range services {
		l := p.Log.With(zap.String("service", s.Name))
		l.Info(ctx, "listing incidents for service")
		listOut, err := p.Client.ListIncidentsWithContext(ctx, pagerduty.ListIncidentsOptions{})
		if err != nil {
			return fmt.Errorf("unable to list incidents for %s: %w", s.Name, err)
		}
		l.Info(ctx, "list finished", zap.Int("len", len(listOut.Incidents)))
	}
	return nil
}