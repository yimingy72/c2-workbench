package implant

import _ "embed"

//go:embed src/implant.c
var Source string

//go:embed src/ops.c
var Ops string

//go:embed src/implant.h
var Header string
