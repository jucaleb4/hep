// Copyright 2019 The go-hep Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package rcont

import (
	"reflect"

	"go-hep.org/x/hep/groot/rbase"
	"go-hep.org/x/hep/groot/rbytes"
	"go-hep.org/x/hep/groot/root"
	"go-hep.org/x/hep/groot/rtypes"
	"go-hep.org/x/hep/groot/rvers"
)

// Map is a ROOT associative array of (key,value) pairs.
// Keys and values must implement the root.Object interface.
type Map struct {
	obj   rbase.Object
	named rbase.Named
	tbl   map[root.Object]root.Object
}

func NewMap() *Map {
	return &Map{
		obj:   *rbase.NewObject(),
		named: *rbase.NewNamed("TMap", "A (key,value) map"),
		tbl:   make(map[root.Object]root.Object),
	}
}

func (*Map) RVersion() int16 { return rvers.Map }
func (*Map) Class() string   { return "TMap" }

func (m *Map) Name() string          { return m.named.Name() }
func (m *Map) Title() string         { return m.named.Title() }
func (m *Map) SetName(name string)   { m.named.SetName(name) }
func (m *Map) SetTitle(title string) { m.named.SetTitle(title) }

// Table returns the underlying hash table.
func (m *Map) Table() map[root.Object]root.Object { return m.tbl }

// ROOTMarshaler is the interface implemented by an object that can
// marshal itself to a ROOT buffer
func (m *Map) MarshalROOT(w *rbytes.WBuffer) (int, error) {
	if w.Err() != nil {
		return 0, w.Err()
	}

	pos := w.WriteVersion(m.RVersion())
	m.obj.MarshalROOT(w)
	m.named.MarshalROOT(w)

	w.WriteI32(int32(len(m.tbl)))

	for k, v := range m.tbl {
		w.WriteObjectAny(k)
		w.WriteObjectAny(v)
	}

	return w.SetByteCount(pos, "TMap")
}

// ROOTUnmarshaler is the interface implemented by an object that can
// unmarshal itself from a ROOT buffer
func (m *Map) UnmarshalROOT(r *rbytes.RBuffer) error {
	if r.Err() != nil {
		return r.Err()
	}

	start := r.Pos()
	vers, pos, bcnt := r.ReadVersion()

	if vers > 2 {
		if err := m.obj.UnmarshalROOT(r); err != nil {
			return err
		}
	}
	if vers > 1 {
		if err := m.named.UnmarshalROOT(r); err != nil {
			return err
		}
	}

	nobjs := int(r.ReadI32())
	m.tbl = make(map[root.Object]root.Object, nobjs)
	for i := 0; i < nobjs; i++ {
		k := r.ReadObjectAny()
		if r.Err() != nil {
			return r.Err()
		}
		v := r.ReadObjectAny()
		if r.Err() != nil {
			return r.Err()
		}
		if k != nil {
			m.tbl[k] = v
		}
	}

	r.CheckByteCount(pos, bcnt, start, "TMap")
	return r.Err()
}

func init() {
	f := func() reflect.Value {
		o := NewMap()
		return reflect.ValueOf(o)
	}
	rtypes.Factory.Add("TMap", f)
}

var (
	_ root.Object        = (*Map)(nil)
	_ root.Named         = (*Map)(nil)
	_ rbytes.Marshaler   = (*Map)(nil)
	_ rbytes.Unmarshaler = (*Map)(nil)
)