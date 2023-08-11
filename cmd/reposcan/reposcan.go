package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/shurcooL/githubv4"
	"github.com/snabb/isoweek"
	"golang.org/x/oauth2"
)

const version = "1.0"

type Settings struct {
	Contributors struct {
		Cooldown  int      `json:"cooldown"`
		Allowlist []string `json:"allowlist"`
	} `json:"contributors"`
	PR struct {
		High int `json:"high"`
		Low  int `json:"low"`
	} `json:"pr"`
	Graphs struct {
		Start *string `json:"start"`
	} `json:"graphs"`
}

type Config struct {
	Settings Settings `json:"settings"`
	Repos    []string `json:"repos"`
}

func main() {
	fmt.Printf("reposcan v%s\n", version)

	fmt.Printf("loading token...\n")

	data, err := os.ReadFile(".token")
	if err != nil {
		fmt.Println("Error opening token file:", err)
		return
	}
	token := strings.Trim(string(data), "\n\r")

	fmt.Printf("loading config...\n")

	jsonData, err := os.ReadFile("config.json")
	if err != nil {
		fmt.Println("Error reading file:", err)
		return
	}

	var config Config
	err = json.Unmarshal(jsonData, &config)
	if err != nil {
		fmt.Println("Error unmarshaling JSON data:", err)
		return
	}

	fmt.Println("authenticating...")

	ctx := context.Background()
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(ctx, ts)
	client := githubv4.NewClient(tc)

	repos := make(map[string]*Repo)
	users := make(map[string]User)

	startGraphs := time.Now().UTC()

	// Load PRs from repos
	for _, k := range config.Repos {
		org, repo, err := orgRepoSplit(k)
		if err != nil {
			fmt.Println("Invalid repo:", err)
			return
		}

		// Get all PRs for this repo
		start, prs, err := repoPulls(ctx, client, org, repo)
		if err != nil {
			fmt.Println("Error reading PRs:", err)
			return
		}

		// No pulse data yet we first need to figure out the
		// earliest start date to align all graphs
		repos[k] = &Repo{
			prs: prs,
		}

		if startGraphs.After(start) {
			// Capture the earliest repo creation time
			startGraphs = start
		}
	}

	// Override for start
	if config.Settings.Graphs.Start != nil {
		startGraphs, err = time.Parse("2006-01-02", *config.Settings.Graphs.Start)
		if err != nil {
			fmt.Println("Error parsing starting time:", err)
			return
		}
	}

	// Generate pulse data
	for _, k := range config.Repos {
		org, repo, err := orgRepoSplit(k)
		if err != nil {
			fmt.Println("Invalid repo:", err)
			return
		}
		fmt.Printf("%s/%s: generating pulse metrics...\n", org, repo)

		endTime := time.Now().AddDate(0, 0, 1)
		repoUsers := getUsers(config, repos[k].prs)
		pulses := getPulses(config, startGraphs, endTime, repos[k].prs, repoUsers)

		// Merge with global user list (we will export this for help building allowlists)
		for k, v := range repoUsers {
			users[k] = v
		}

		repos[k].pulses = pulses
		repos[k].start = startGraphs

		fmt.Printf("%s/%s: generating pr graph...\n", org, repo)

		err = genPRGraph(org, repo, repos[k].pulses)
		if err != nil {
			fmt.Println("Error writing PR graph:", err)
			return
		}

		fmt.Printf("%s/%s: generating normalised graph...\n", org, repo)

		err = genNormGraph(org, repo, repos[k].pulses)
		if err != nil {
			fmt.Println("Error writing normalised graph:", err)
			return
		}
	}

	err = genCompareNormGraphs(config, repos)
	if err != nil {
		fmt.Println("Error writing normalised comparison graphs:", err)
		return
	}

	fmt.Printf("generating user list...\n")
	err = genUsers(users)
	if err != nil {
		fmt.Println("Error writing users to file:", err)
		return
	}

	fmt.Println("done.")
}

func genCompareNormGraphs(config Config, repos map[string]*Repo) error {
	graphs := []struct {
		name string
		desc string
	}{
		{
			name: "open",
			desc: "open (norm)",
		},
		{
			name: "merged",
			desc: "merged (norm)",
		},
	}

	for _, t := range graphs {

		fmt.Printf("%s: generating normalised comparison graph...\n", t.desc)

		name := fmt.Sprintf("compare-%s.csv", t.name)
		f, err := os.Create(name)
		if err != nil {
			return fmt.Errorf("cannot create graph file: %w", err)
		}

		w := csv.NewWriter(f)
		w.Write([]string{fmt.Sprintf("Compare: %s", t.desc)})

		for i, k := range config.Repos {
			if i == 0 {
				// The first iteration needs to plot the dates
				line := make([]string, 0)
				line = append(line, "Pulse")
				for _, v := range repos[k].pulses {
					line = append(line, v.Start.Format("2006-01-02"))
				}
				w.Write(line)
			}

			line := make([]string, 0)
			line = append(line, k)
			for _, v := range repos[k].pulses {
				line = append(line, func(t string, p Pulse) string {
					switch t {
					case "open":
						return fmt.Sprintf("%0.2f", p.PrOpenNorm)
					case "merged":
						return fmt.Sprintf("%0.2f", p.PrMergedNorm)
					default:
						panic("not a valid metric type")
					}
				}(t.name, v))
			}
			w.Write(line)
		}

		w.Flush()
		f.Sync()
		f.Close()
	}
	return nil
}

func genPRGraph(org string, repo string, pulses []Pulse) error {

	name := fmt.Sprintf("%s-%s-abs.csv", org, repo)
	f, err := os.Create(name)
	if err != nil {
		return fmt.Errorf("cannot create graph file: %w", err)
	}

	w := csv.NewWriter(f)
	w.Write([]string{fmt.Sprintf("Repo: %s/%s", org, repo)})
	w.Write([]string{
		"Pulse",
		"Contributors",
		"Open",
		"Merged",
	})
	for _, p := range pulses {

		s := p.Start.Format("2006-01-02")
		w.Write([]string{
			s,
			fmt.Sprintf("%d", p.Contributors),
			fmt.Sprintf("%0.2f", p.PrOpen),
			fmt.Sprintf("%0.2f", p.PrMerged),
		})
	}
	w.Flush()
	f.Sync()
	f.Close()
	return nil
}

func genNormGraph(org string, repo string, pulses []Pulse) error {

	name := fmt.Sprintf("%s-%s-norm.csv", org, repo)
	f, err := os.Create(name)
	if err != nil {
		return fmt.Errorf("cannot create graph file: %w", err)
	}

	w := csv.NewWriter(f)
	w.Write([]string{fmt.Sprintf("Repo: %s/%s", org, repo)})
	w.Write([]string{
		"Pulse",
		"Open (Norm)",
		"Merged (Norm)",
	})
	for _, p := range pulses {

		s := p.Start.Format("2006-01-02")
		w.Write([]string{
			s,
			fmt.Sprintf("%0.2f", p.PrOpenNorm),
			fmt.Sprintf("%0.2f", p.PrMergedNorm),
		})
	}
	w.Flush()
	f.Sync()
	f.Close()
	return nil
}

func genUsers(users map[string]User) error {

	name := fmt.Sprintf("all-users.csv")
	f, err := os.Create(name)
	if err != nil {
		return fmt.Errorf("cannot create user list file: %w", err)
	}

	w := csv.NewWriter(f)
	w.Write([]string{"Login"})
	for k, _ := range users {
		w.Write([]string{k})
	}
	w.Flush()
	f.Sync()
	f.Close()
	return nil
}

type Repo struct {
	start  time.Time
	prs    []PrEntry
	pulses []Pulse
}

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

func repoPulls(ctx context.Context, client *githubv4.Client, org string, repo string) (start time.Time, prs []PrEntry, err error) {
	var q RepoEntry

	variables := map[string]interface{}{
		"owner":       githubv4.String(org),
		"name":        githubv4.String(repo),
		"nodesCursor": (*githubv4.String)(nil),
	}
	done := 0
	total := 0
	for {
		err := client.Query(ctx, &q, variables)
		if err != nil {
			return start, prs, fmt.Errorf("repo requests failed: %w\n", err)
		}

		prs = append(prs, q.Repository.PullRequests.Nodes...)

		done += 100
		total = q.Repository.PullRequests.TotalCount
		if done < total {
			fmt.Printf("\r%s/%s: reading pr history (%d/%d)...", org, repo, done, total)
		}

		if !q.Repository.PullRequests.PageInfo.HasNextPage {
			break
		}
		variables["nodesCursor"] = githubv4.NewString(q.Repository.PullRequests.PageInfo.EndCursor)
	}

	fmt.Printf("\r%s/%s: reading pr history (%d/%d)...\n", org, repo, total, total)

	return q.Repository.CreatedAt, prs, nil
}

func orgRepoSplit(key string) (org string, repo string, err error) {
	elements := strings.Split(key, "/")
	if len(elements) == 2 {
		return elements[0], elements[1], nil
	}
	return "", "", fmt.Errorf("repo JSON key invalid")
}

type User struct {
	Start time.Time
	End   time.Time
}

func getUsers(config Config, pulls []PrEntry) map[string]User {
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
		// in the last x months
		cooldown := config.Settings.Contributors.Cooldown * 30 * 24
		if time.Now().Sub(endTime) < (time.Duration(cooldown) * time.Hour) {
			endTime = time.Now().UTC()
		}

		users[login] = User{
			Start: startTime,
			End:   endTime,
		}
	}
	return users
}

func allowlistedUser(config Config, login string) bool {
	if len(config.Settings.Contributors.Allowlist) == 0 {
		// Empty list means all users are tracked
		return true
	}

	for _, u := range config.Settings.Contributors.Allowlist {
		if u == login {
			return true
		}
	}
	return false
}

func pulseContributors(config Config, users map[string]User, start time.Time, end time.Time) (contributors int) {
	for k, v := range users {
		if allowlistedUser(config, k) == false {
			// Ignore this user
			continue
		}

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

func pulsePulls(config Config, pulls []PrEntry, start time.Time, end time.Time) []Pull {
	pull := make([]Pull, 0)
	for _, p := range pulls {
		// Only pulls by allowlisted users are tracked
		if allowlistedUser(config, p.Author.Login) == false {
			continue
		}

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

func prSizeWeight(config Config, lines float32) float32 {
	if lines > float32(config.Settings.PR.High) {
		return 3.0
	} else if lines > float32(config.Settings.PR.Low) {
		return 2.0
	}
	return 1.0
}

func getOpen(config Config, pulls []Pull) float32 {
	var count float32
	for _, p := range pulls {
		if p.Open == true {
			count += 1.0
		}
	}
	return count
}

func getOpenNorm(config Config, pulls []Pull, con int) float32 {
	var count float32
	for _, p := range pulls {
		if p.Open == true {
			count += prSizeWeight(config,float32(p.Lines))
		}
	}
	if con == 0 {
		return 0.0
	}
	return count / float32(con)
}

func getMerged(config Config, pulls []Pull) float32 {
	var count float32
	for _, p := range pulls {
		if p.Merged == true {
			count += 1.0
		}
	}
	return count
}

func getMergedNorm(config Config, pulls []Pull, con int) float32 {
	var count float32
	for _, p := range pulls {
		if p.Merged == true {
			count += prSizeWeight(config, float32(p.Lines))
		}
	}
	if con == 0 {
		return 0.0
	}
	return count / float32(con)
}

type Pulse struct {
	Start          time.Time
	End            time.Time // Start time of the following week
	Days           int
	Contributors   int
	PrOpen         float32
	PrMerged       float32
	PrOpenNorm     float32
	PrMergedNorm   float32
}

func isoWeeks(year int) (weeks int) {
	// Day 28 always on last ISO week of current year
	_, weeks = time.Date(year, 12, 28, 0, 0, 0, 0, time.UTC).ISOWeek()
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

func getPulses(config Config, start time.Time, end time.Time, pulls []PrEntry, users map[string]User) []Pulse {
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

		people := pulseContributors(config, users, s, e)
		pulsePulls := pulsePulls(config, pulls, s, e)

		pulses = append(pulses, Pulse{
			Start:          s,
			End:            e,
			Days:           d,
			Contributors:   people,
			PrOpen:         getOpen(config, pulsePulls),
			PrMerged:       getMerged(config, pulsePulls),
			PrOpenNorm:     getOpenNorm(config, pulsePulls, people),
			PrMergedNorm:   getMergedNorm(config, pulsePulls, people),
		})

		yearStart = yearEnd
		weekStart = weekEnd
	}
	return pulses
}
