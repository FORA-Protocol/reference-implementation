package dashboard

import (
	"fmt"
	"strconv"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
)

// The chart's drawing box, in SVG user units. The SVG scales to its column, so
// these fix proportions, not pixels.
const (
	chartWidth  = 720.0
	chartHeight = 220.0
	// The plot sits inside the box, leaving room for the y ticks on the left
	// and the day labels below.
	plotLeft  = 36.0
	plotRight = chartWidth - 8
	plotTop   = 12.0
	// The labels take two lines under the axis on a long period: the month,
	// and under it the year where one begins.
	plotBottom = chartHeight - 50
	// A column is at most this wide, however few days the period has: a
	// column never fills its slot, and the leftover is air.
	maxColumnWidth = 24.0
	// Columns in adjacent slots stay this far apart, while that leaves a
	// column at least minColumnShare of its slot. On a long period the slots
	// are narrower than the gap, and the gap gives way.
	columnGap      = 2.0
	minColumnShare = 0.6
	// The data end of a column is rounded; the baseline end is square.
	columnRadius = 4.0
	// labelEvery is how often a day gets a label under the axis on a short
	// period. The first day always does.
	labelEvery = 7
	// monthlyLabelsAfter is the longest period labelled by the week. A longer
	// one is labelled at the first of each month, because a label every seven
	// days would put fifty of them in one line.
	monthlyLabelsAfter = 62
)

// Chart is transactions per day, drawn from the totals buckets the Exchange
// returned. It is one series, so it has no legend: the heading says what it
// plots.
type Chart struct {
	Width, Height float64
	// Baseline and PlotLeft, PlotRight bound the plot, for the axis line.
	Baseline, PlotLeft, PlotRight float64
	Columns                       []Column
	Ticks                         []Tick
}

// Column is one day. Path is the column's outline, empty for a day with no
// transactions; the hit area is the day's whole slot, so an empty day has a
// tooltip too.
type Column struct {
	Day   string
	Label string
	// SubLabel is the year, under the first label and under each January on
	// a period labelled by the month.
	SubLabel string
	Count    int64
	Path     string
	SlotX    float64
	SlotW    float64
	// LabelX is where the day's label is centred, when it has one.
	LabelX float64
}

// Tick is one horizontal gridline and its value.
type Tick struct {
	Y     float64
	Value string
}

// newChart lays out one column per bucket, scaled to the busiest day.
func newChart(buckets []exchangeclient.RevenueBucket) Chart {
	c := Chart{
		Width: chartWidth, Height: chartHeight,
		Baseline: plotBottom, PlotLeft: plotLeft, PlotRight: plotRight,
	}
	if len(buckets) == 0 {
		return c
	}
	var busiest int64
	for _, b := range buckets {
		busiest = max(busiest, b.TransactionCount)
	}
	top := niceCeiling(busiest)
	c.Ticks = ticks(top)
	slot := (plotRight - plotLeft) / float64(len(buckets))
	width := min(maxColumnWidth, max(slot-columnGap, slot*minColumnShare))
	monthly := len(buckets) > monthlyLabelsAfter
	for i, b := range buckets {
		x := plotLeft + float64(i)*slot
		col := Column{
			Day:   b.Start.UTC().Format("2 Jan 2006"),
			Count: b.TransactionCount,
			SlotX: x, SlotW: slot,
		}
		col.Label, col.SubLabel = dayLabel(b.Start.UTC(), i, monthly)
		if col.Label != "" {
			col.LabelX = x + slot/2
		}
		if b.TransactionCount > 0 {
			h := float64(b.TransactionCount) / float64(top) * (plotBottom - plotTop)
			col.Path = columnPath(x+(slot-width)/2, width, h)
		}
		c.Columns = append(c.Columns, col)
	}
	return c
}

// dayLabel is what the day at index i is labelled with under the axis, if
// anything. A short period is labelled every seventh day; a long one at the
// first of each month, with the year under the first label and each January.
func dayLabel(day time.Time, i int, monthly bool) (label, year string) {
	if !monthly {
		if i%labelEvery == 0 {
			return day.Format("2 Jan"), ""
		}
		return "", ""
	}
	if i != 0 && day.Day() != 1 {
		return "", ""
	}
	if i == 0 || day.Month() == time.January {
		year = day.Format("2006")
	}
	return day.Format("Jan"), year
}

// columnPath is a column of width w and height h standing on the baseline at
// x, with its top corners rounded.
func columnPath(x, w, h float64) string {
	r := min(columnRadius, w/2, h)
	y := plotBottom - h
	return fmt.Sprintf("M%s %sV%sQ%s %s %s %sH%sQ%s %s %s %sV%sZ",
		num(x), num(plotBottom), num(y+r),
		num(x), num(y), num(x+r), num(y),
		num(x+w-r),
		num(x+w), num(y), num(x+w), num(y+r),
		num(plotBottom))
}

// niceCeiling is the smallest round number at or above n: 1, 2 or 5 times a
// power of ten. A day with no transactions anywhere still gets a scale of 1.
func niceCeiling(n int64) int64 {
	if n <= 1 {
		return 1
	}
	for step := int64(1); ; step *= 10 {
		for _, m := range []int64{1, 2, 5} {
			if m*step >= n {
				return m * step
			}
		}
	}
}

// ticks are the gridlines for a scale topping out at top: the baseline, the
// top, and the halfway line when it falls on a whole count.
func ticks(top int64) []Tick {
	values := []int64{0, top}
	if top%2 == 0 {
		values = []int64{0, top / 2, top}
	}
	out := make([]Tick, 0, len(values))
	for _, v := range values {
		y := plotBottom - float64(v)/float64(top)*(plotBottom-plotTop)
		out = append(out, Tick{Y: y, Value: strconv.FormatInt(v, 10)})
	}
	return out
}

// num writes a coordinate with no more precision than a screen can show.
func num(f float64) string {
	return strconv.FormatFloat(f, 'f', 1, 64)
}
