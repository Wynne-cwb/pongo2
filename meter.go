package pongo2

import (
	"io"
	"reflect"
)

// Meter bounds what one execution of a template may cost: loop iterations,
// the size of the values it builds, the bytes it writes, and which functions
// it may call. An execution without a meter behaves exactly as before.
//
// A Meter's methods return an error to stop the execution; the execution
// fails with that error as its OrigError.
type Meter interface {
	// ChargeIteration is called before each iteration of a for loop.
	ChargeIteration() error
	// ChargeValue is called with the estimated size in bytes of a value a
	// filter, the + operator or a function call is about to build or has
	// returned. Filters whose result size follows from their arguments
	// (center, ljust, rjust, add) are charged before they run.
	ChargeValue(bytes int) error
	// ChargeOutput is called before bytes are written to the output.
	ChargeOutput(bytes int) error
	// CheckCall is called before a function is called. name is the
	// identifier called; topLevel is true only when it is a name of the
	// public context called directly with parentheses, and false for a
	// method of a value, a function a value holds, a name a tag assigned
	// (with, set, for), or a function referenced without parentheses, which
	// pongo2 calls all the same.
	CheckCall(name string, topLevel bool) error
}

// ExecuteWriterUnbufferedMetered is ExecuteWriterUnbuffered under meter: the
// execution charges meter as it runs and stops at the first error meter
// returns. A nil meter is ExecuteWriterUnbuffered.
func (tpl *Template) ExecuteWriterUnbufferedMetered(context Context, writer io.Writer, meter Meter) error {
	if meter == nil {
		return tpl.ExecuteWriterUnbuffered(context, writer)
	}
	parent, ctx, err := tpl.newContextForExecution(context)
	if err != nil {
		return err
	}
	ctx.Meter = meter
	mw := &meteredWriter{w: writer, meter: meter}
	if err := parent.root.Execute(ctx, mw); err != nil {
		return err
	}
	if mw.err != nil {
		return &Error{Template: tpl, Filename: tpl.name, Sender: "execution", OrigError: mw.err}
	}
	return nil
}

// meteredWriter charges the meter before it writes. Nodes do not read a
// write's error, so the first refusal is kept: nothing more is written, the
// next loop iteration stops, and the execution fails with it.
type meteredWriter struct {
	w     io.Writer
	meter Meter
	err   error
}

func (m *meteredWriter) Write(p []byte) (int, error) {
	if m.err != nil {
		return 0, m.err
	}
	if err := m.meter.ChargeOutput(len(p)); err != nil {
		m.err = err
		return 0, err
	}
	return m.w.Write(p)
}

func (m *meteredWriter) WriteString(s string) (int, error) {
	if m.err != nil {
		return 0, m.err
	}
	if err := m.meter.ChargeOutput(len(s)); err != nil {
		m.err = err
		return 0, err
	}
	return io.WriteString(m.w, s)
}

// writeRefused is the refusal a metered writer kept, if writer is one.
func writeRefused(writer TemplateWriter) error {
	if mw, ok := writer.(*meteredWriter); ok {
		return mw.err
	}
	return nil
}

// chargeValue charges ctx's meter, if any, for a value of bytes.
func (ctx *ExecutionContext) chargeValue(bytes int, token *Token) *Error {
	if ctx.Meter == nil {
		return nil
	}
	if err := ctx.Meter.ChargeValue(bytes); err != nil {
		return ctx.OrigError(err, token)
	}
	return nil
}

// valueSizeDepth bounds how deep EstimateSize looks into nested values.
const valueSizeDepth = 4

// EstimateSize is a cheap estimate of the bytes v holds: a string's length,
// the sum of a list's or map's items (8 bytes for any other scalar), looking
// at most a few levels deep.
func EstimateSize(v *Value) int {
	if v == nil {
		return 0
	}
	return estimateReflectSize(v.getResolvedValue(), valueSizeDepth)
}

func estimateReflectSize(rv reflect.Value, depth int) int {
	const scalar = 8
	if !rv.IsValid() {
		return 0
	}
	if rv.Kind() == reflect.Interface || rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return 0
		}
		rv = rv.Elem()
	}
	if rv.Type() == reflect.TypeOf(Value{}) {
		return estimateReflectSize(rv.Addr().Interface().(*Value).getResolvedValue(), depth)
	}
	switch rv.Kind() {
	case reflect.String:
		return rv.Len()
	case reflect.Slice, reflect.Array:
		if depth == 0 {
			return rv.Len() * scalar
		}
		size := 0
		for i := 0; i < rv.Len(); i++ {
			size += estimateReflectSize(rv.Index(i), depth-1)
		}
		return size
	case reflect.Map:
		if depth == 0 {
			return rv.Len() * 2 * scalar
		}
		size := 0
		iter := rv.MapRange()
		for iter.Next() {
			size += estimateReflectSize(iter.Key(), depth-1) + estimateReflectSize(iter.Value(), depth-1)
		}
		return size
	case reflect.Struct:
		return rv.NumField() * scalar
	default:
		return scalar
	}
}
