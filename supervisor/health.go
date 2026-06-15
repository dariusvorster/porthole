package supervisor

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/porthole/porthole/engine"
)

// HealthState is the surfaced probe state. "" means not yet probed / no policy.
type HealthState string

const (
	HealthStarting  HealthState = "starting"
	HealthHealthy   HealthState = "healthy"
	HealthUnhealthy HealthState = "unhealthy"
)

// HealthInfo is the health slice of the supervision SSE event.
type HealthInfo struct {
	State     HealthState `json:"state"`
	LastProbe *time.Time  `json:"lastProbe,omitempty"`
	Failures  int         `json:"failures"`
}

// healthRuntime is the carried per-container probe state.
type healthRuntime struct {
	State     HealthState
	Failures  int
	LastProbe time.Time
	probing   bool // a probe goroutine is in flight
}

const (
	healthTypeLabel = "porthole.health.type"
	healthDefInterval = 30 * time.Second
	healthDefTimeout  = 5 * time.Second
	healthDefRetries  = 3
	healthDefStart    = 10 * time.Second
)

func (h HealthPolicy) interval() time.Duration {
	if h.Interval > 0 {
		return time.Duration(h.Interval) * time.Second
	}
	return healthDefInterval
}

func (h HealthPolicy) timeout() time.Duration {
	if h.Timeout > 0 {
		return time.Duration(h.Timeout) * time.Second
	}
	return healthDefTimeout
}

func (h HealthPolicy) retries() int {
	if h.Retries > 0 {
		return h.Retries
	}
	return healthDefRetries
}

func (h HealthPolicy) startPeriod() time.Duration {
	if h.StartPeriod > 0 {
		return time.Duration(h.StartPeriod) * time.Second
	}
	return healthDefStart
}

// healthFromLabels builds a HealthPolicy from porthole.health.* labels, or nil if
// no valid health type is present (spec §5: config via labels or the store).
func healthFromLabels(labels map[string]string) *HealthPolicy {
	t := labels[healthTypeLabel]
	if t != "http" && t != "tcp" {
		return nil
	}
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	return &HealthPolicy{
		Type:        t,
		Port:        atoi(labels["porthole.health.port"]),
		Path:        labels["porthole.health.path"],
		Interval:    atoi(labels["porthole.health.interval"]),
		Timeout:     atoi(labels["porthole.health.timeout"]),
		Retries:     atoi(labels["porthole.health.retries"]),
		StartPeriod: atoi(labels["porthole.health.start-period"]),
		OnUnhealthy: labels["porthole.health.on-unhealthy"],
	}
}

// probeTarget resolves where to probe: the dedicated vmnet IP directly (primary,
// §9.3), else 127.0.0.1:<publishedHostPort> for the matching container port.
func probeTarget(c engine.Container, hp HealthPolicy) (host string, port int, ok bool) {
	if ip := c.PrimaryIPv4(); ip != "" {
		return ip, hp.Port, true
	}
	for _, p := range c.Configuration.PublishedPorts {
		if p.ContainerPort == hp.Port && p.HostPort != 0 {
			return "127.0.0.1", p.HostPort, true
		}
	}
	return "", 0, false
}

func probeHTTP(ctx context.Context, host string, port int, path string) bool {
	url := fmt.Sprintf("http://%s:%d%s", host, port, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

func probeTCP(ctx context.Context, host string, port int, timeout time.Duration) bool {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// decideHealth is the pure probe state machine: given a probe result and whether
// we're still in the start-period, return the next runtime. Healthy stays healthy
// until `retries` consecutive failures flip it to unhealthy; start-period grace
// means failures don't count yet.
func decideHealth(pass, withinStart bool, prev healthRuntime, hp HealthPolicy, now time.Time) healthRuntime {
	next := prev
	next.LastProbe = now
	if next.State == "" {
		next.State = HealthStarting
	}
	if pass {
		next.Failures = 0
		next.State = HealthHealthy
		return next
	}
	if withinStart {
		return next // grace: a failure during start-period doesn't count
	}
	next.Failures++
	if next.Failures >= hp.retries() {
		next.State = HealthUnhealthy
	}
	// else: keep current state (healthy stays healthy until the streak hits retries)
	return next
}

// maybeProbe schedules a probe for a running container if one is due and none is
// in flight. Probes run in a goroutine so a slow/timing-out probe never blocks
// the reconcile poll.
func (s *Supervisor) maybeProbe(c engine.Container, hp HealthPolicy, now time.Time) {
	if !c.IsRunning() {
		s.mu.Lock()
		if hr := s.health[c.ID]; hr != nil {
			*hr = healthRuntime{} // health only exists while running with an IP
		}
		s.mu.Unlock()
		return
	}
	host, port, ok := probeTarget(c, hp)
	if !ok {
		return
	}

	s.mu.Lock()
	hr := s.health[c.ID]
	if hr == nil {
		hr = &healthRuntime{}
		s.health[c.ID] = hr
	}
	if hr.State == "" {
		hr.State = HealthStarting
	}
	due := hr.LastProbe.IsZero() || now.Sub(hr.LastProbe) >= hp.interval()
	if hr.probing || !due {
		s.mu.Unlock()
		return
	}
	hr.probing = true
	s.mu.Unlock()

	go s.runProbe(c, hp, host, port)
}

func (s *Supervisor) runProbe(c engine.Container, hp HealthPolicy, host string, port int) {
	ctx, cancel := context.WithTimeout(context.Background(), hp.timeout())
	defer cancel()

	var pass bool
	if hp.Type == "http" {
		pass = probeHTTP(ctx, host, port, hp.Path)
	} else {
		pass = probeTCP(ctx, host, port, hp.timeout())
	}

	now := s.nowFn()
	withinStart := !c.Status.StartedDate.IsZero() && now.Sub(c.Status.StartedDate) < hp.startPeriod()

	s.mu.Lock()
	hr := s.health[c.ID]
	if hr == nil {
		hr = &healthRuntime{}
		s.health[c.ID] = hr
	}
	prevState := hr.State
	*hr = decideHealth(pass, withinStart, *hr, hp, now)
	hr.probing = false
	newState := hr.State

	restart := false
	gaveUpNow := false
	if newState == HealthUnhealthy && hp.OnUnhealthy == "restart" {
		wasGaveUp := s.state[c.ID].GaveUp
		var act Action
		act, s.state[c.ID] = decideUnhealthy(s.state[c.ID], s.cfg, now)
		restart = act == ActionRestart
		gaveUpNow = act == ActionGiveUp && !wasGaveUp
	}
	s.mu.Unlock()

	if restart {
		s.logger.Printf("supervisor: health-restart %s (unhealthy)", c.ID)
		s.doFullRestart(c.ID)
	}
	if newState != prevState || restart || gaveUpNow {
		s.emitFor(c.ID)
	}
}
