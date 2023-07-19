# reposcan

Generate metrics from your repositories...

## Build

```
go build ./cmd/reposcan
```

## Authentication

Please generate a personal access token (classic) with repo and user access. Place the token in the same directory as the reposcan binary in a file called: ```.token```

## Generated CSV data

CSV files are generated in the current directory.

The easiest way to use them is to open a Google Sheets document in your browser, and then from the menu select "Import...". You can import multiple CSV files into their own sheet in the same document. Finally, select a data collection and select "Chart" from the menu.

Note: You may have to play around with the chart settings to make it work.

### Pulses

Data is currently by organised into 2-week pulses. This is based on ISO Weeks, and therefore week 1 may start in the previous year, or the last week may end in the next year. The start date supplied for graphs in the config will be modified to allign with the previous start of a pulse. A pulse will start on ISO week 1, 3 , 5 etc...

### Metrics: Open

Number of open PRs as measured by the end of a pulse.

### Metrics: Churn

Number of open PRs closed without merging during a pulse.

### Metrics: Merged

Number of open PRs merged during a pulse.

### Metrics: Velocity

Difference between Merged and Churn (merged - churn)

### Metrics: Normalisation

In order to compare results between repos, we have to perform some normalisation to make the comparison fair.

Currently, normalisation considers two aspects: team size and pr size

PR size multiplier (configurable):
```
< 50  lines: 1x
> 50  lines: 2x
> 500 lines: 3x
```

The scanner attemps to keep a good idea of how large the contributor base is over the life of the project. The current algorithm is very basic, and considers a contributor active from their first PR until their last PR, with a configurable cool-down period at the end.

The number of PRs in a pulse are each scaled by the size multiplier and then the total pulse weight divided by the number of active contributors.

## Config

The behaviour of reposcan is controlled with a JSON config file:

```
{
  "settings": {
    "contributors": {

      // If there is a gap between the last PR and the current
      // date, the contributor will be considered part of the
      // team as long as this period is less months than the
      // supplied cooldown period (months). Currently no gaps
      // are inserted for contributors two have large gaps between
      // PRs on the timeline.
      "cooldown": 1,

      // Filter statistics by only considering PRs created by
      // the following list of people (using te Github login name).
      "allowlist": []
    },
    "pr": {

      // If the number of lines of a PR is above this number, a
      // multiply factor if 3x is applied. This is only used for
      // normalised data.
      "high": 500,
      
      // If the number of lines of a PR is above this number, a
      // multiply factor if 2x is applied, else if below, a 1x
      // factor is applied. This is only used for normalised data.
      "low": 50
    },
    "graphs": {

      // This may be null, or if a date is supplied, the graphs will
      // be forced to start on the supplied data. Note that all
      // graphs always start on the same data, irrespective if data
      // is available or not.
      "start": "2022-01-01"
    }

  },
  "repos": [

    // Make sure your personal access token (classic only) has access
    // to all the specified repositories.

    "snapcore/snapcraft",
    "snapcore/spread",
    "snapcore/snapd",
    "canonical/chisel",
    "canonical/pebble"
  ]
}
```
