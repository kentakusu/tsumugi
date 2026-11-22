package main

// 並び順を保つ JSON のオブジェクト（設定ファイルを書き戻すとき、鍵の順を変えて差分を汚さないため）。

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type oobj struct {
	keys []string
	vals map[string]any // 値は *oobj か、そのほかの JSON の値
}

func newOobj() *oobj { return &oobj{vals: map[string]any{}} }

func (o *oobj) set(k string, v any) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func parseOrdered(b []byte) (*oobj, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	v, err := readValue(dec)
	if err != nil {
		return nil, err
	}
	o, ok := v.(*oobj)
	if !ok {
		return nil, fmt.Errorf("一番上がオブジェクトではありません")
	}
	return o, nil
}

func readValue(dec *json.Decoder) (any, error) {
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch d := t.(type) {
	case json.Delim:
		switch d {
		case '{':
			o := newOobj()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				v, err := readValue(dec)
				if err != nil {
					return nil, err
				}
				o.set(kt.(string), v)
			}
			_, err := dec.Token()
			return o, err
		case '[':
			a := []any{} // ★nil にすると、空の配列 [] が null で書き戻される（設定を壊す）
			for dec.More() {
				v, err := readValue(dec)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			_, err := dec.Token()
			return a, err
		}
	}
	return t, nil
}

func (o *oobj) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := json.Marshal(o.vals[k])
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// 普通の map に開く（比べるため）。
func (o *oobj) plain() map[string]any {
	m := map[string]any{}
	for _, k := range o.keys {
		m[k] = plainValue(o.vals[k])
	}
	return m
}

func plainValue(v any) any {
	switch x := v.(type) {
	case *oobj:
		return x.plain()
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = plainValue(e)
		}
		return out
	case json.Number:
		var f any
		json.Unmarshal([]byte(x.String()), &f)
		return f
	}
	return v
}

// a.b.c に値を置く（無い途中の段は作る。順は末尾に足す）。
func (o *oobj) setLeaf(path []string, v any) {
	cur := o
	for _, p := range path[:len(path)-1] {
		next, ok := cur.vals[p].(*oobj)
		if !ok {
			next = newOobj()
			cur.set(p, next)
		}
		cur = next
	}
	cur.set(path[len(path)-1], v)
}
