package host

import (
	"cmp"
	"context"
	"encoding/json"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// sessionKey is where the boundary leaves a request's ticket hash; requestKey is where the SDK's logger leaves the request id that its usage records carry as their trace id.
const sessionKey, requestKey = "ao_session", "__request_id__"

// Activity counts what passes through each account. Only the token counts are saved, under a hash of the account's id, which may hold an email.
type Activity struct {
	mu       sync.Mutex
	path     string
	changed  bool
	accounts map[string]*tally
	requests sync.Map // request id → ticket hash, while the request runs and a minute after
}

// tally is one account's counts; the exported fields are the saved ones.
type tally struct {
	Since string                      `json:"since"`
	Total int64                       `json:"total"`
	Days  map[string]map[string]int64 `json:"days"` // local day → model → tokens

	day      string
	sessions map[string]int64 // ticket hash → tokens on day
	failures []event          // the last three hours
	firsts   []event          // the last hour's streamed requests
	last     *event
}

// event is a failed request with its upstream status, or a streamed request with its milliseconds to the first token.
type event struct {
	at time.Time
	n  int
}

// OpenActivity starts from the saved counts when they are readable, and empty otherwise.
func OpenActivity(path string) *Activity {
	a := &Activity{path: path}
	data, _ := os.ReadFile(path)
	if json.Unmarshal(data, &a.accounts) != nil || a.accounts == nil {
		a.accounts = map[string]*tally{}
	}
	maps.DeleteFunc(a.accounts, func(_ string, t *tally) bool { return t == nil || t.Days == nil })
	return a
}

// bind names a request's session by the SDK's request id: a usage record is handled after its request and carries nothing else of it.
func (a *Activity) bind(c *gin.Context) {
	if id, session := c.GetString(requestKey), c.GetString(sessionKey); id != "" && session != "" {
		a.requests.Store(id, session)
		defer time.AfterFunc(time.Minute, func() { a.requests.Delete(id) })
	}
	c.Next()
}

// HandleUsage counts one upstream request; the SDK calls it from its dispatcher.
func (a *Activity) HandleUsage(_ context.Context, r coreusage.Record) {
	at, d, account := cmp.Or(r.RequestedAt, time.Now()), r.Detail, TicketHash(r.AuthID)
	tokens, day := cmp.Or(max(d.TotalTokens, 0), d.InputTokens+d.OutputTokens+d.CacheReadTokens+d.CacheCreationTokens), at.Local().Format(time.DateOnly)
	session, _ := a.requests.Load(r.TraceID)
	a.mu.Lock()
	defer a.mu.Unlock()
	t := cmp.Or(a.accounts[account], &tally{Days: map[string]map[string]int64{}})
	a.accounts[account] = t
	if r.Failed && r.Fail.StatusCode != 499 { // 499 is the user stopping the agent, not a refusal
		t.last = &event{at, r.Fail.StatusCode}
		t.failures = append(since(t.failures, at.Add(-3*time.Hour)), *t.last)
	}
	if r.Stream && r.TTFT > 0 {
		t.firsts = append(since(t.firsts, at.Add(-time.Hour)), event{at, int(r.TTFT.Milliseconds())})
	}
	// Counting tokens and listing models generate nothing.
	if tokens <= 0 || !coreusage.GenerateEnabled(r.Generate) {
		return
	}
	if t.Days[day] == nil {
		t.Days[day] = map[string]int64{}
	}
	t.Days[day][r.Model] += tokens
	t.Total, t.Since, a.changed = t.Total+tokens, cmp.Or(t.Since, day), true
	if day > t.day {
		t.day, t.sessions = day, map[string]int64{}
	}
	if hash, ok := session.(string); ok && day == t.day {
		t.sessions[hash] += tokens
	}
}
func failureKind(status int) string {
	if status >= 500 || status <= 0 {
		return "server"
	}
	return cmp.Or(map[int]string{429: "limit", 401: "sign-in", 403: "sign-in"}[status], "other")
}
func since(events []event, from time.Time) []event {
	return slices.DeleteFunc(events, func(e event) bool { return e.at.Before(from) })
}

// report adds an account's activity and health to its state, leaving out what there is nothing to say about.
func (a *Activity) report(out gin.H, id string, now time.Time) gin.H {
	a.mu.Lock()
	defer a.mu.Unlock()
	t := cmp.Or(a.accounts[TicketHash(id)], &tally{})
	if t.Since != "" {
		activity, days, models, week, top := gin.H{"total": t.Total, "since": t.Since}, []gin.H{}, map[string]int64{}, int64(0), []gin.H{}
		for back := 13; back >= 0; back-- {
			day, sum := now.Local().AddDate(0, 0, -back).Format(time.DateOnly), int64(0)
			for model, tokens := range t.Days[day] {
				if sum += tokens; back < 7 {
					models[model] += tokens
				}
			}
			days = append(days, gin.H{"date": day, "tokens": sum})
		}
		for i, name := range slices.SortedFunc(maps.Keys(models), func(x, y string) int { return cmp.Or(cmp.Compare(models[y], models[x]), cmp.Compare(x, y)) }) {
			if week += models[name]; i < 6 {
				top = append(top, gin.H{"model": name, "tokens": models[name]})
				activity["models"] = top
			}
		}
		if t.day == days[13]["date"] && len(t.sessions) > 0 {
			activity["sessions"] = maps.Clone(t.sessions)
		}
		activity["today"], activity["week"], activity["days"], out["activity"] = days[13]["tokens"], week, days, activity
	}
	t.failures, t.firsts = since(t.failures, now.Add(-3*time.Hour)), since(t.firsts, now.Add(-time.Hour))
	health, kinds := gin.H{}, map[string]int{}
	if t.last != nil {
		health["lastFailure"] = gin.H{"kind": failureKind(t.last.n), "at": t.last.at.UTC().Format(time.RFC3339), "status": t.last.n}
	}
	for _, failure := range t.failures {
		kinds[strings.Replace(failureKind(failure.n), "sign-in", "signIn", 1)]++
		health["failures"] = kinds
	}
	// The median: the middle one of the times in order.
	if slices.SortFunc(t.firsts, func(x, y event) int { return x.n - y.n }); len(t.firsts) > 0 {
		health["firstWordMs"] = t.firsts[len(t.firsts)/2].n
	}
	if len(health) > 0 {
		out["health"] = health
	}
	return out
}

// save writes the token counts when they changed, keeping thirty days of them.
func (a *Activity) save() {
	a.mu.Lock()
	defer a.mu.Unlock()
	oldest := time.Now().AddDate(0, 0, -29).Format(time.DateOnly)
	for _, t := range a.accounts {
		maps.DeleteFunc(t.Days, func(day string, _ map[string]int64) bool { return day < oldest })
	}
	if data, _ := json.Marshal(a.accounts); a.changed {
		a.changed = writePrivate(a.path, data) != nil
	}
}
