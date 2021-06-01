package pdscrape

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	io_prometheus_client "github.com/prometheus/client_model/go"

	"github.com/PagerDuty/go-pagerduty"
	"github.com/cresta/zapctx"
	"go.uber.org/zap"
)

type PdScrape struct {
	Client           *pagerduty.Client
	Log              *zapctx.Logger
	LookbackDuration time.Duration
	k                knownIncidents
	s                knownServices
	lastSyncTime     atomicTime
}

func (p *PdScrape) CreateGather(timeRange time.Duration) prometheus.Gatherer {
	return prometheus.GathererFunc(func() ([]*io_prometheus_client.MetricFamily, error) {
		percentFree := prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "pdcollector",
			Subsystem: "incidents",
			Name:      "free_percent",
			Help:      "% time [0-1] of no incidents in this timerange",
		}, []string{"service", "timerange", "id", "team"})
		scrapeAge := prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "pdcollector",
			Subsystem: "scrape",
			Name:      "age_seconds",
			Help:      "How long ago the last scraped occurred",
		})
		incidentCounts := prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "pdcollector",
			Subsystem: "incidents",
			Name:      "status_amount",
			Help:      "# of incidents in this timerange by their current status",
		}, []string{"service", "timerange", "id", "status", "team"})
		ctx := context.Background()
		vals, err := p.Availabilities(ctx, timeRange)
		if err != nil {
			return nil, fmt.Errorf("unable to fetch availabilities: %w", err)
		}
		for s, v := range vals {
			percentFree.WithLabelValues(p.s.nameForID(s), timeRange.String(), s, p.s.teamForId(s)).Set(float64(v) / float64(time.Hour*24))
		}
		counts, err := p.IncidentCounts(ctx, timeRange)
		if err != nil {
			return nil, fmt.Errorf("unable to get incident counts: %w", err)
		}
		for s, c := range counts {
			incidentCounts.WithLabelValues(p.s.nameForID(s), timeRange.String(), s, "triggered", p.s.teamForId(s)).Set(float64(c.Triggered))
			incidentCounts.WithLabelValues(p.s.nameForID(s), timeRange.String(), s, "acknowledged", p.s.teamForId(s)).Set(float64(c.Acknowledged))
			incidentCounts.WithLabelValues(p.s.nameForID(s), timeRange.String(), s, "resolved", p.s.teamForId(s)).Set(float64(c.Resolved))
		}
		scrapeAge.Set(time.Since(p.lastSyncTime.get()).Seconds())
		r := prometheus.NewRegistry()
		if err := r.Register(percentFree); err != nil {
			return nil, fmt.Errorf("unable to register collector percent_free: %w", err)
		}
		if err := r.Register(incidentCounts); err != nil {
			return nil, fmt.Errorf("unable to register collector incident_counts: %w", err)
		}
		if err := r.Register(scrapeAge); err != nil {
			return nil, fmt.Errorf("unable to register collector scrape_age: %w", err)
		}
		return r.Gather()
	})
}

type atomicTime struct {
	t  time.Time
	mu sync.Mutex
}

func (a *atomicTime) set(t time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.t = t
}

func (a *atomicTime) get() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.t
}

func (p *PdScrape) lookbackDuration() time.Duration {
	if p.LookbackDuration == 0 {
		return time.Hour * 24 * 7
	}
	return p.LookbackDuration
}

func (p *PdScrape) Init(ctx context.Context, token string) error {
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

func (p *PdScrape) refreshIncidentsWithSort(ctx context.Context, sortBy string, currentTime time.Time, status []string, lookbackDuration time.Duration) error {
	var prev pagerduty.APIListObject
	prev.Limit = 1000

	var startTime string
	if lookbackDuration == 0 {
		startTime = ""
	} else {
		startTime = currentTime.Add(-lookbackDuration).UTC().String()
	}
	for {
		inc, err := p.Client.ListIncidentsWithContext(ctx, pagerduty.ListIncidentsOptions{
			APIListObject: prev,
			Since:         startTime,
			Statuses:      status,
			SortBy:        sortBy,
		})
		if err != nil {
			return fmt.Errorf("unable to list more incidents: %w", err)
		}
		p.Log.Info(ctx, "got incidents", zap.Any("page", inc.APIListObject))
		for idx, i := range inc.Incidents {
			if p.k.addIncident(i) {
				p.Log.Info(ctx, "breaking early", zap.Int("idx", idx))
				return nil
			}
		}
		if !inc.More {
			break
		}
		prev = inc.APIListObject
		prev.Offset += prev.Limit
	}
	return nil
}

func (p *PdScrape) refreshIncidents(ctx context.Context, currentTime time.Time) error {
	// Note: They have the documentation "Return only incidents with the given statuses. (More status codes may be introduced in the future.)"
	//       We will need to update this if they add more status codes.
	//
	// Insert even old events, if they are active
	if err := p.refreshIncidentsWithSort(ctx, "created_at:desc", currentTime, []string{"triggered", "acknowledged"}, 0); err != nil {
		return fmt.Errorf("unable to refresh incidents by created_at: %w", err)
	}
	// Insert recent events that are resolved
	if err := p.refreshIncidentsWithSort(ctx, "created_at:desc", currentTime, []string{"resolved"}, p.lookbackDuration()); err != nil {
		return fmt.Errorf("unable to refresh incidents by created_at: %w", err)
	}
	if err := p.refreshIncidentsWithSort(ctx, "resolved_at:desc", currentTime, []string{"resolved"}, p.lookbackDuration()); err != nil {
		return fmt.Errorf("unable to refresh incidents by updated_at: %w", err)
	}
	return nil
}

func (p *PdScrape) Scrape(ctx context.Context) error {
	currentTime := time.Now().UTC()
	p.Log.Info(ctx, "Listing services")
	services, err := p.Client.ListServicesPaginated(ctx, pagerduty.ListServiceOptions{
		Includes: []string{"teams"},
	})
	if err != nil {
		return fmt.Errorf("unable to list services: %w", err)
	}
	p.Log.Info(ctx, "got services", zap.Int("len", len(services)))
	err = p.refreshIncidents(ctx, currentTime)
	if err != nil {
		return fmt.Errorf("unable to list all incidents: %w", err)
	}
	p.s.set(services)
	p.lastSyncTime.set(currentTime)
	return nil
}

func (p *PdScrape) Availabilities(ctx context.Context, lookback time.Duration) (map[string]time.Duration, error) {
	p.Log.Debug(ctx, "got incs", zap.Int("len", len(p.k.incidentByID)))
	currentTime := p.lastSyncTime.get()
	byRange, err := p.k.allRangesByService(currentTime)
	if err != nil {
		return nil, fmt.Errorf("unable to create all service ranges: %w", err)
	}
	p.Log.Debug(ctx, "got ranges", zap.Int("len", len(byRange)))
	ret := make(map[string]time.Duration)
	for _, s := range p.s.get() {
		l := p.Log.With(zap.String("service", s.Name), zap.String("service_id", s.ID))
		l.Debug(ctx, "on service")
		oneDayRange := timeRange{
			start: currentTime.Add(-lookback),
			end:   currentTime,
		}
		avail := rangeList{ranges: []timeRange{oneDayRange}}
		l.Debug(ctx, "incidents for service", zap.Int("len", len(byRange[s.ID])))
		for _, inc := range byRange[s.ID] {
			avail.sub(inc.timeRange)
		}
		totalAvail := avail.totalTime()
		l.Debug(ctx, "total availability past 24 hours", zap.Duration("time", totalAvail))
		ret[s.ID] = totalAvail
	}
	return ret, nil
}

type IncidentCounts struct {
	Triggered    int
	Acknowledged int
	Resolved     int
}

func (p *PdScrape) IncidentCounts(ctx context.Context, lookback time.Duration) (map[string]IncidentCounts, error) {
	p.Log.Debug(ctx, "got incs", zap.Int("len", len(p.k.incidentByID)))
	currentTime := p.lastSyncTime.get()
	byRange, err := p.k.allRangesByService(currentTime)
	if err != nil {
		return nil, fmt.Errorf("unable to create all service ranges: %w", err)
	}
	p.Log.Debug(ctx, "got ranges", zap.Int("len", len(byRange)))
	ret := make(map[string]IncidentCounts)
	for _, s := range p.s.get() {
		var ic IncidentCounts
		for _, inc := range byRange[s.ID] {
			switch inc.inc.Status {
			case "triggered":
				ic.Triggered++
			case "acknowledged":
				ic.Acknowledged++
			case "resolved":
				statChange, err := time.Parse(time.RFC3339, inc.inc.LastStatusChangeAt)
				if err != nil {
					return nil, fmt.Errorf("unable to parse last status change for incident %s of %s: %w", inc.inc.Id, inc.inc.LastStatusChangeAt, err)
				}
				if statChange.Add(lookback).After(currentTime) {
					ic.Resolved++
				}
			default:
				return nil, fmt.Errorf("invalid incident status for %s of %s", inc.inc.Id, inc.inc.Status)
			}
		}
		ret[s.ID] = ic
	}
	return ret, nil
}

type knownIncidents struct {
	incidentByID map[string]pagerduty.Incident
	mu           sync.Mutex
}

func (k *knownIncidents) allRangesByService(currentTime time.Time) (map[string][]incidentRange, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	ret := make(map[string][]incidentRange)
	for _, i := range k.incidentByID {
		ir, err := rangeFromIncident(i, currentTime)
		if err != nil {
			return nil, fmt.Errorf("unable to parse incident %s: %w", i.Id, err)
		}
		ret[i.Service.ID] = append(ret[i.Service.ID], ir)
	}
	return ret, nil
}

// addIncident will add it to the set.  Returns true if it already existed and was unchanged
func (k *knownIncidents) addIncident(i pagerduty.Incident) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.incidentByID == nil {
		k.incidentByID = make(map[string]pagerduty.Incident)
	}
	if _, exists := k.incidentByID[i.Id]; !exists {
		k.incidentByID[i.Id] = i
		return false
	}
	if k.incidentByID[i.Id].LastStatusChangeAt == i.LastStatusChangeAt {
		return true
	}
	k.incidentByID[i.Id] = i
	return false
}

type timeRange struct {
	start time.Time
	end   time.Time
}

func (t *timeRange) String() string {
	return fmt.Sprintf("[%s <-> %s]", t.start.String(), t.end.String())
}

type rangeList struct {
	ranges []timeRange
}

func (t *rangeList) totalTime() time.Duration {
	ret := time.Duration(0)
	for _, r := range t.ranges {
		if r.end.Before(r.start) {
			panic("Invalid setup for total time")
		}
		ret += r.end.Sub(r.start)
	}
	return ret
}

func (t *rangeList) String() string {
	p := make([]string, 0, len(t.ranges))
	for _, r := range t.ranges {
		p = append(p, r.String())
	}
	return fmt.Sprintf("{%s}", strings.Join(p, "-"))
}

func (t *rangeList) sub(toRemove timeRange) {
	if toRemove.start.After(toRemove.end) {
		panic("Invalid incident for subtraction")
	}
	if len(t.ranges) == 0 {
		return
	}
	newRanges := make([]timeRange, 0, len(t.ranges))
	for _, includedRange := range t.ranges {
		if toRemove.start.After(includedRange.end) {
			// R = ------***--
			// I = --***------
			// Fully added because removed block after this block
			newRanges = append(newRanges, includedRange)
			continue
		}
		if toRemove.end.Before(includedRange.start) {
			// R = --***------
			// I = ------***--
			// Fully added because removed block before this block
			newRanges = append(newRanges, includedRange)
			continue
		}
		if toRemove.start.Before(includedRange.start) {
			if toRemove.end.After(includedRange.end) {
				// R = -****------
				// I = --**-------
				// Fully removed
				continue
			}
			// R = -**------
			// I = --****---
			newRanges = append(newRanges, timeRange{
				start: toRemove.end,
				end:   includedRange.end,
			})
			continue
		}
		if toRemove.end.After(includedRange.end) {
			// R = ---****--
			// I = --****---
			newRanges = append(newRanges, timeRange{
				start: includedRange.start,
				end:   toRemove.start,
			})
			continue
		}
		// R = ---**----
		// I = --****---
		newRanges = append(newRanges, timeRange{
			start: includedRange.start,
			end:   toRemove.start,
		}, timeRange{
			start: toRemove.end,
			end:   includedRange.end,
		})
	}
	t.ranges = newRanges
}

type incidentRange struct {
	timeRange
	inc pagerduty.Incident
}

func rangeFromIncident(i pagerduty.Incident, currentTime time.Time) (incidentRange, error) {
	startTime, err := time.Parse(time.RFC3339, i.CreatedAt)
	if err != nil {
		return incidentRange{}, fmt.Errorf("unable to parse start time %s: %w", i.CreatedAt, err)
	}
	endTime, err := time.Parse(time.RFC3339, i.LastStatusChangeAt)
	if err != nil {
		return incidentRange{}, fmt.Errorf("unable to parse end time %s: %w", i.LastStatusChangeAt, err)
	}
	if i.Status != "resolved" {
		endTime = currentTime
	}
	return incidentRange{
		timeRange: timeRange{
			start: startTime,
			end:   endTime,
		},
		inc: i,
	}, nil
}

type knownServices struct {
	s  []pagerduty.Service
	mu sync.Mutex
}

func (k *knownServices) nameForID(id string) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, s := range k.s {
		if s.ID == id {
			return s.Name
		}
	}
	return ""
}

func (k *knownServices) teamForId(id string) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, s := range k.s {
		if s.ID == id {
			if len(s.Teams) > 0 {
				// Note: Only one team supported.  What would multiple teams look like in /metrics?
				return s.Teams[0].Name
			}
			return ""
		}
	}
	return ""
}

func (k *knownServices) set(s []pagerduty.Service) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.s = s
}

func (k *knownServices) get() []pagerduty.Service {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.s
}
