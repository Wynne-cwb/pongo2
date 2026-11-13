package pongo2_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/carlosxl/pongo2/v6"
)

var errStop = errors.New("meter: stop")

// recordingMeter counts what an execution charges and stops past its limits.
type recordingMeter struct {
	iterations, maxIterations int
	values, maxValueBytes     int
	output, maxOutputBytes    int
	calls                     []string
	refuse                    func(name string, topLevel bool) bool
}

func (m *recordingMeter) ChargeIteration() error {
	m.iterations++
	if m.maxIterations > 0 && m.iterations > m.maxIterations {
		return errStop
	}
	return nil
}

func (m *recordingMeter) ChargeValue(bytes int) error {
	m.values += bytes
	if m.maxValueBytes > 0 && m.values > m.maxValueBytes {
		return errStop
	}
	return nil
}

func (m *recordingMeter) ChargeOutput(bytes int) error {
	m.output += bytes
	if m.maxOutputBytes > 0 && m.output > m.maxOutputBytes {
		return errStop
	}
	return nil
}

func (m *recordingMeter) CheckCall(name string, topLevel bool) error {
	if topLevel {
		m.calls = append(m.calls, name)
	} else {
		m.calls = append(m.calls, "~"+name)
	}
	if m.refuse != nil && m.refuse(name, topLevel) {
		return errStop
	}
	return nil
}

type order struct{ Items []string }

func (o order) Count() int { return len(o.Items) }

func execMetered(t *testing.T, tpl string, ctx pongo2.Context, meter pongo2.Meter) (string, error) {
	t.Helper()
	template, err := pongo2.FromString(tpl)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var out strings.Builder
	err = template.ExecuteWriterUnbufferedMetered(ctx, &out, meter)
	return out.String(), err
}

func TestMeterChargesIterationsAndStops(t *testing.T) {
	meter := &recordingMeter{maxIterations: 3}
	_, err := execMetered(t, `{% for i in items %}{{ i }}{% endfor %}`, pongo2.Context{"items": []int{1, 2, 3, 4, 5}}, meter)
	if err == nil || !strings.Contains(err.Error(), errStop.Error()) {
		t.Fatalf("want the meter's stop, got %v", err)
	}
	if meter.iterations != 4 {
		t.Fatalf("iterations charged: %d", meter.iterations)
	}
}

func TestMeterChargesAWidthFilterBeforeItBuildsTheValue(t *testing.T) {
	meter := &recordingMeter{maxValueBytes: 1000}
	_, err := execMetered(t, `{{ "x"|center:100000000 }}`, nil, meter)
	if err == nil || !strings.Contains(err.Error(), errStop.Error()) {
		t.Fatalf("want the meter's stop, got %v", err)
	}
	if meter.values != 100000000 {
		t.Fatalf("charged %d before building", meter.values)
	}
}

func TestMeterChargesPlusAndOutput(t *testing.T) {
	meter := &recordingMeter{}
	out, err := execMetered(t, `{{ a + b }}`, pongo2.Context{"a": "abc", "b": "de"}, meter)
	if err != nil || out != "abcde" {
		t.Fatalf("got %q, %v", out, err)
	}
	if meter.values < 5 || meter.output != 5 {
		t.Fatalf("values %d output %d", meter.values, meter.output)
	}
}

func TestMeterStopsOutput(t *testing.T) {
	meter := &recordingMeter{maxOutputBytes: 10}
	_, err := execMetered(t, `{% for i in items %}0123456789{% endfor %}`, pongo2.Context{"items": []int{1, 2}}, meter)
	if err == nil {
		t.Fatal("want the meter's stop")
	}
}

func TestMeterTellsTopLevelCallsFromTheRest(t *testing.T) {
	meter := &recordingMeter{}
	ctx := pongo2.Context{
		"top":   func(s string) string { return s },
		"order": order{Items: []string{"a", "b"}},
	}
	_, err := execMetered(t, `{{ top("x") }}{{ order.Count() }}{{ order.Count }}{{ missing(1) }}`, ctx, meter)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	want := []string{"top", "~Count", "~Count", "missing"}
	if strings.Join(meter.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls %v, want %v", meter.calls, want)
	}
}

func TestMeterRefusesACall(t *testing.T) {
	meter := &recordingMeter{refuse: func(_ string, topLevel bool) bool { return !topLevel }}
	_, err := execMetered(t, `{{ order.Count() }}`, pongo2.Context{"order": order{}}, meter)
	if err == nil || !strings.Contains(err.Error(), errStop.Error()) {
		t.Fatalf("want the meter's refusal, got %v", err)
	}
}

func TestNoMeterIsTodaysExecution(t *testing.T) {
	template := pongo2.Must(pongo2.FromString(`{% for i in items %}{{ i }}{% endfor %}{{ "x"|center:5 }}`))
	var out strings.Builder
	if err := template.ExecuteWriterUnbufferedMetered(pongo2.Context{"items": []int{1, 2}}, &out, nil); err != nil {
		t.Fatal(err)
	}
	if out.String() != "12  x  " {
		t.Fatalf("got %q", out.String())
	}
}

func TestMeterSeesAFunctionAssignedThroughWithAsNotADirectCall(t *testing.T) {
	meter := &recordingMeter{refuse: func(_ string, topLevel bool) bool { return !topLevel }}
	_, err := execMetered(t, `{% with f=top %}{{ f }}{% endwith %}`, pongo2.Context{"top": func() string { return "x" }}, meter)
	if err == nil || !strings.Contains(err.Error(), errStop.Error()) {
		t.Fatalf("want the meter's refusal, got %v", err)
	}
	if strings.Join(meter.calls, ",") != "~top" {
		t.Fatalf("calls %v", meter.calls)
	}
}

func TestMeterRefusesACallOfAValueBeforeResolvingIt(t *testing.T) {
	for _, tpl := range []string{`{{ data.items.append(1) }}`, `{% with f=data.name %}{{ f(1) }}{% endwith %}`} {
		meter := &recordingMeter{refuse: func(_ string, topLevel bool) bool { return !topLevel }}
		_, err := execMetered(t, tpl, pongo2.Context{"data": map[string]any{"items": []any{1}, "name": "x"}}, meter)
		if err == nil || !strings.Contains(err.Error(), errStop.Error()) {
			t.Fatalf("%s: want the meter's refusal, got %v", tpl, err)
		}
	}
}

// A block that renders its body into its own buffer (spaceless, filter,
// ifchanged) is metered as it renders: the loop inside stops at the output
// limit instead of filling the buffer until the block ends.
func TestMeterStopsOutputInsideABufferingBlock(t *testing.T) {
	for name, tpl := range map[string]string{
		"spaceless": `{% spaceless %}{% for i in items %}0123456789{% endfor %}{% endspaceless %}`,
		"filter":    `{% filter lower %}{% for i in items %}0123456789{% endfor %}{% endfilter %}`,
		"ifchanged": `{% for j in once %}{% ifchanged %}{% for i in items %}0123456789{% endfor %}{% endifchanged %}{% endfor %}`,
	} {
		t.Run(name, func(t *testing.T) {
			meter := &recordingMeter{maxOutputBytes: 100}
			items := make([]int, 10000)
			_, err := execMetered(t, tpl, pongo2.Context{"items": items, "once": []int{1}}, meter)
			if err == nil {
				t.Fatal("want the meter's stop")
			}
			if meter.iterations > 20 {
				t.Fatalf("the loop ran %d iterations past a 100-byte output limit; it must stop as it crosses it", meter.iterations)
			}
		})
	}
}

func TestMeterIterationRefusalIsReportedAtTheLoopsLine(t *testing.T) {
	meter := &recordingMeter{maxIterations: 3}
	_, err := execMetered(t, "<p>a</p>\n<p>b</p>\n{% for i in items %}{% for j in items %}.{% endfor %}{% endfor %}",
		pongo2.Context{"items": []int{1, 2, 3}}, meter)
	var perr *pongo2.Error
	if !errors.As(err, &perr) {
		t.Fatalf("want a pongo2 error, got %v", err)
	}
	if perr.Line != 3 {
		t.Fatalf("line: want 3, got %d", perr.Line)
	}
	if !errors.Is(perr.OrigError, errStop) {
		t.Fatalf("want the meter's stop as the cause, got %v", perr.OrigError)
	}
}
