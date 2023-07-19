package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/shurcooL/githubv4"
	"github.com/snabb/isoweek"
	"golang.org/x/oauth2"
)

const (
	version              = "1.0"
	pr_high              = 500
	pr_low               = 50
	contributor_cooldown = 1 // month
)

var (
	org   string
	repo  string
	token string
)

type PrEntry struct {
	Additions int
	ClosedAt  *time.Time
	CreatedAt time.Time
	MergedAt  *time.Time
	Deletions int
	State     string
	Author    struct {
		Login string
	}
}

type RepoEntry struct {
	Repository struct {
		CreatedAt    time.Time
		PullRequests struct {
			Nodes    []PrEntry
			PageInfo struct {
				EndCursor   githubv4.String
				HasNextPage bool
			}
			TotalCount int
		} `graphql:"pullRequests(first: 100, after: $nodesCursor)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

func main() {
	// Load the token if available
	data, _ := os.ReadFile(".token")
	token = strings.Trim(string(data), "\n\r")

	flag.StringVar(&org, "org", "canonical", "github organisation")
	flag.StringVar(&repo, "repo", "pebble", "github repository")
	flag.StringVar(&token, "token", token, "github personal access token")
	flag.Parse()

	fmt.Printf("reposcan v%s\n", version)
	fmt.Println()
	fmt.Printf("org: %s\n", org)
	fmt.Printf("repo: %s\n", repo)
	fmt.Printf("token: %s\n", token)
	fmt.Println()

	fmt.Println("authenticating...")
	ctx := context.Background()
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(ctx, ts)
	client := githubv4.NewClient(tc)

	fmt.Print("requesting pull request history...")
	var q RepoEntry
	var prs []PrEntry
	variables := map[string]interface{}{
		"owner":       githubv4.String(org),
		"name":        githubv4.String(repo),
		"nodesCursor": (*githubv4.String)(nil),
	}
	done := 0
	for {
		err := client.Query(ctx, &q, variables)
		if err != nil {
			fmt.Printf("repo requests failed: %v\n", err)
			return
		}
		prs = append(prs, q.Repository.PullRequests.Nodes...)
		if !q.Repository.PullRequests.PageInfo.HasNextPage {
			break
		}
		variables["nodesCursor"] = githubv4.NewString(q.Repository.PullRequests.PageInfo.EndCursor)
		done += 100
		fmt.Printf("\rrequesting pull request history... [%d/%d]", done, q.Repository.PullRequests.TotalCount)
	}

	startTime := q.Repository.CreatedAt
	endTime := time.Now().AddDate(0, 0, 1)
	pulses := getPulses(startTime, endTime, prs)
	fmt.Println("")

	fmt.Println("generating absolute graph...")
	// Normal CSV
	name := fmt.Sprintf("%s-%s-abs.csv", org, repo)
	f, err := os.Create(name)
	if err != nil {
		panic("cannot create file")
	}

	w := csv.NewWriter(f)
	w.Write([]string{
		"Pulse",
		"Contributors",
		"Open",
		"Churn",
		"Merged",
		"Velocity",
	})
	for _, p := range pulses {

		s := p.Start.Format("2006-01-02")
		w.Write([]string{
			s,
			fmt.Sprintf("%d", p.Contributors),
			fmt.Sprintf("%0.2f", p.PrOpen),
			fmt.Sprintf("%0.2f", -p.PrChurn),
			fmt.Sprintf("%0.2f", p.PrMerged),
			fmt.Sprintf("%0.2f", p.PrVelocity),
		})
	}
	w.Flush()
	f.Sync()
	f.Close()

	fmt.Println("generating team/pr size normalised graph...")
	// Normalised CSV
	name = fmt.Sprintf("%s-%s-norm.csv", org, repo)
	f, err = os.Create(name)
	if err != nil {
		panic("cannot create file")
	}

	w = csv.NewWriter(f)
	w.Write([]string{
		"Pulse",
		"Open (Norm)",
		"Churn (Norm)",
		"Merged (Norm)",
		"Velocity (Norm)",
	})
	for _, p := range pulses {

		s := p.Start.Format("2006-01-02")
		w.Write([]string{
			s,
			fmt.Sprintf("%0.2f", p.PrOpenNorm),
			fmt.Sprintf("%0.2f", -p.PrChurnNorm),
			fmt.Sprintf("%0.2f", p.PrMergedNorm),
			fmt.Sprintf("%0.2f", p.PrVelocityNorm),
		})
	}
	w.Flush()
	f.Sync()
	f.Close()
}

type User struct {
	Start time.Time
	End   time.Time
}

func getUsers(pulls []PrEntry) map[string]User {
	users := make(map[string]User)
	for _, r := range pulls {
		if r.Author.Login == "" {
			continue
		}
		login := r.Author.Login

		if strings.HasPrefix(login, "renovate") {
			continue
		}

		var endTime time.Time
		if r.MergedAt != nil {
			endTime = *r.MergedAt
		} else if r.ClosedAt != nil {
			endTime = *r.ClosedAt
		} else {
			endTime = time.Now().UTC()
		}
		startTime := r.CreatedAt

		// Update existing
		if val, ok := users[login]; ok {
			if val.End.After(endTime) {
				endTime = val.End
			}
			if val.Start.Before(startTime) {
				startTime = val.Start
			}
		}

		// Promote to current time if user contributed
		// in the last 1 months
		if time.Now().Sub(endTime) < (contributor_cooldown * 30 * 24 * time.Hour) {
			endTime = time.Now().UTC()
		}

		users[login] = User{
			Start: startTime,
			End:   endTime,
		}
	}
	return users
}

func pulseContributors(users map[string]User, start time.Time, end time.Time) (contributors int) {
	for _, v := range users {
		if v.Start.Before(end) == true && v.End.Before(start) == false {
			contributors = contributors + 1
		}
	}
	return contributors
}

type Pull struct {
	Merged bool
	Closed bool
	Open   bool
	Lines  int
}

func pulsePulls(pulls []PrEntry, start time.Time, end time.Time) []Pull {
	pull := make([]Pull, 0)
	for _, p := range pulls {
		// All PRs that overlap with the window
		if (p.ClosedAt == nil || p.ClosedAt.Before(start) == false) && p.CreatedAt.Before(end) == true {
			// All PRs that closed within the window
			if p.State != "OPEN" {
				if p.ClosedAt.Before(start) == false && p.ClosedAt.Before(end) == true {
					merged := (p.MergedAt != nil)
					lines := p.Additions + p.Deletions
					pull = append(pull, Pull{
						Merged: merged,
						Closed: !merged,
						Open:   false,
						Lines:  lines,
					})
				}
			} else {
				// Open PRs inside the window
				lines := p.Additions + p.Deletions
				pull = append(pull, Pull{
					Merged: false,
					Closed: false,
					Open:   true,
					Lines:  lines,
				})
			}
		}
	}
	return pull
}

func prSizeWeight(lines float32) float32 {
	if lines > float32(pr_high) {
		return 3.0
	} else if lines > float32(pr_low) {
		return 2.0
	}
	return 1.0
}

func getOpen(pulls []Pull, norm bool, con int) float32 {
	var open float32
	var count float32
	for _, p := range pulls {
		if p.Open == true {
			count += 1.0
			open += float32(p.Lines)
		}
	}
	// Average Lines
	open = open / count

	if norm == false {
		return count
	}
	if con == 0 {
		return 0.0
	}
	return count * prSizeWeight(open) / float32(con)
}

func getChurn(pulls []Pull, norm bool, con int) float32 {
	var churn float32
	var count float32
	for _, p := range pulls {
		if p.Closed == true {
			count += 1.0
			churn += float32(p.Lines)
		}
	}
	// Average Lines
	churn = churn / count

	if norm == false {
		return count
	}
	if con == 0 {
		return 0.0
	}
	return count * prSizeWeight(churn) / float32(con)
}

func getMerged(pulls []Pull, norm bool, con int) float32 {
	var merged float32
	var count float32
	for _, p := range pulls {
		if p.Merged == true {
			count += 1.0
			merged += float32(p.Lines)
		}
	}
	// Average Lines
	merged = merged / count

	if norm == false {
		return count
	}
	if con == 0 {
		return 0.0
	}
	return count * prSizeWeight(merged) / float32(con)
}

type Pulse struct {
	Start          time.Time
	End            time.Time // Start time of the following week
	Days           int
	Contributors   int
	PrOpen         float32
	PrChurn        float32
	PrMerged       float32
	PrVelocity     float32
	PrOpenNorm     float32
	PrChurnNorm    float32
	PrMergedNorm   float32
	PrVelocityNorm float32
}

func isoWeeks(year int) (weeks int) {
	_, weeks = time.Date(year, 12, 31, 0, 0, 0, 0, time.UTC).ISOWeek()
	return weeks
}

func isoWeekToPulseStart(week int) int {
	if (week & 1) == 0 {
		week = week - 1
	}
	if week <= 0 {
		panic("iso week start must be 1 or higher")
	}
	return week
}

func nextPulseToIsoWeek(year int, week int) (int, int) {
	week = week + 2
	// The next into a new year must be on week 1
	if week > isoWeeks(year) {
		year = year + 1
		week = 1
	}
	return year, week
}

func getPulses(start time.Time, end time.Time, pulls []PrEntry) []Pulse {
	users := getUsers(pulls)
	if end.Before(start) {
		panic("end time cannot before start")
	}

	// For now assume 2-week pulses start on the 1st ISO week of the year
	yearStart, weekStart := start.ISOWeek()
	weekStart = isoWeekToPulseStart(weekStart)

	pulses := make([]Pulse, 0)
	for {
		s := isoweek.StartTime(yearStart, weekStart, time.UTC)
		yearEnd, weekEnd := nextPulseToIsoWeek(yearStart, weekStart)
		e := isoweek.StartTime(yearEnd, weekEnd, time.UTC)
		d := int(e.Sub(s).Hours()) / 24
		if s.After(end) {
			break
		}

		people := pulseContributors(users, s, e)
		pulsePulls := pulsePulls(pulls, s, e)

		pulses = append(pulses, Pulse{
			Start:          s,
			End:            e,
			Days:           d,
			Contributors:   people,
			PrOpen:         getOpen(pulsePulls, false, 0),
			PrChurn:        getChurn(pulsePulls, false, 0),
			PrMerged:       getMerged(pulsePulls, false, 0),
			PrVelocity:     (getMerged(pulsePulls, false, 0) - getChurn(pulsePulls, false, 0)),
			PrOpenNorm:     getOpen(pulsePulls, true, people),
			PrChurnNorm:    getChurn(pulsePulls, true, people),
			PrMergedNorm:   getMerged(pulsePulls, true, people),
			PrVelocityNorm: (getMerged(pulsePulls, true, people) - getChurn(pulsePulls, true, people)),
		})

		yearStart = yearEnd
		weekStart = weekEnd
	}
	return pulses
}
