package main

import (
	"fmt"
	"strings"

	"github.com/richardwilkes/toolbox/log/jot"
	"github.com/richardwilkes/toolbox/txt"
)

var specialNames = map[string]bool{
	"range":  true,
	"select": true,
	"type":   true,
}

type field struct {
	Owner        *structDef
	Name         string
	GoName       string
	CReturnType  string
	GoReturnType string
	CParams      []string
	GoParams     []string
	FunctionPtr  bool
	NeedsUnsafe  bool
	Position     position
}

func newField(owner *structDef, name, typeInfo string, pos position) *field {
	original := typeInfo
	f := &field{
		Owner:    owner,
		Name:     name,
		GoName:   txt.ToCamelCase(name),
		Position: pos,
	}
	if _, ok := specialNames[name]; ok {
		f.Name = "_" + name
	}
	fp := strings.Index(typeInfo, "(*)")
	if fp != -1 {
		f.FunctionPtr = true
		typeInfo = typeInfo[:fp]
	}
	f.CReturnType = filterCTypeName(typeInfo)
	f.GoReturnType = deriveGoTypeFromCType(f.CReturnType, &f.NeedsUnsafe)
	if f.FunctionPtr {
		params := original[fp+3:]
		if !strings.HasPrefix(params, "(") || !strings.HasSuffix(params, ")") {
			jot.Fatalf(1, "Can't handle params in type: %s", original)
		}
		f.CParams = strings.Split(params[1:len(params)-1], ",")
		for i := range f.CParams {
			f.CParams[i] = filterCTypeName(f.CParams[i])
			f.GoParams = append(f.GoParams, deriveGoTypeFromCType(f.CParams[i], &f.NeedsUnsafe))
		}
	}
	return f
}

func filterCTypeName(in string) string {
	in = strings.TrimSpace(in)
	if strings.HasPrefix(in, "const struct _cef_") {
		in = in[14:]
	} else if strings.HasPrefix(in, "struct _cef_") {
		in = in[8:]
	}
	if i := strings.Index(in, "':'"); i != -1 {
		in = in[:i]
	}
	return in
}

type cMapping struct {
	C  string
	Go string
}

var cMappings = []cMapping{
	{C: "cef_string_t", Go: "string"},
	{C: "cef_string_userfree_t", Go: "string"},
	{C: "cef_string_userfree_utf8_t", Go: "string"},
	{C: "cef_string_userfree_utf16_t", Go: "string"},
	{C: "cef_string_userfree_wide_t", Go: "string"},
	{C: "cef_string_utf8_t", Go: "string"},
	{C: "cef_string_utf16_t", Go: "string"},
	{C: "cef_string_wide_t", Go: "string"},
	{C: "size_t", Go: "uint64"},
	{C: "int", Go: "int32"},
	{C: "float", Go: "float32"},
	{C: "double", Go: "float64"},
	{C: "char", Go: "byte"},
	{C: "char16", Go: "int16"},
	{C: "wchar_t", Go: "int16"},
}

func deriveGoTypeFromCType(in string, needsUnsafe *bool) string {
	in = strings.Replace(in, "const ", "", -1)
	switch in {
	case "void":
		return ""
	case "void *":
		*needsUnsafe = true
		return "unsafe.Pointer"
	case "void **":
		*needsUnsafe = true
		return "*unsafe.Pointer"
	case "char **":
		*needsUnsafe = true
		return "[]string"
	default:
		prefix := ""
		if i := strings.Index(in, " "); i != -1 {
			prefix = in[i+1:]
			in = in[:i]
		}
		for _, one := range cMappings {
			if one.C == in {
				return prefix + one.Go
			}
		}
		return prefix + translateStructTypeName(in)
	}
}

func (f *field) Skip() bool {
	return f.Name == "base" && (f.CReturnType == "cef_base_ref_counted_t" || f.CReturnType == "cef_base_scoped_t")
}

func (f *field) ParameterList() string {
	var buffer strings.Builder
	if f.FunctionPtr {
		count := 0
		for i, p := range f.GoParams {
			if i != 0 || p != "*"+f.Owner.GoName {
				count++
				if count != 1 {
					buffer.WriteString(", ")
				}
				fmt.Fprintf(&buffer, "p%d", count)
				if i == len(f.GoParams)-1 || f.GoParams[i+1] != p {
					fmt.Fprintf(&buffer, " %s", p)
				}
			}
		}
	}
	return buffer.String()
}

type param struct {
	Type string
	Ptrs string
}

func (f *field) CallFunctionPointer() string {
	params := make([]param, len(f.CParams))
	for i, p := range f.CParams {
		p := strings.Replace(p, "const ", "", -1)
		if space := strings.Index(p, " "); space != -1 {
			params[i] = param{
				Type: p[:space],
				Ptrs: p[space+1:],
			}
		} else {
			params[i] = param{Type: p}
		}
	}
	var buffer strings.Builder
	for i, p := range params {
		if i != 0 {
			if p.Type == "cef_string_t" {
				fmt.Fprintf(&buffer, "var s%d C.cef_string_t\n", i)
				fmt.Fprintf(&buffer, "setCEFStr(%sp%d, &s%d)\n", p.Ptrs, i, i)
			} else if p.Ptrs == "**" {
				if sdef, exists := sdefsMap[p.Type]; exists {
					fmt.Fprintf(&buffer, "pd%d := (*p%d).toNative(", i, i)
					if !sdef.isClassEquivalent() {
						fmt.Fprintf(&buffer, "&C.%s{}", p)
					}
					buffer.WriteString(")\n")
				} else if p.Type == "char" {
					fmt.Fprintf(&buffer, `cp%[1]d := C.calloc(C.size_t(len(p%[1]d)), C.size_t(unsafe.Sizeof(uintptr(0))))
tp%[1]d := (*[1<<30 - 1]*C.char)(cp%[1]d)
for i, one := range p%[1]d {
	tp%[1]d[i] = C.CString(one)
}
`, i)
				}
			} else if p.Ptrs == "*" {
				if _, exists := edefsMap[p.Type]; exists {
					fmt.Fprintf(&buffer, "e%d := C.%s(*p%d)\n", i, p.Type, i)
				}
			}
		}
	}
	prefixLines := buffer.String()
	buffer.Reset()
	fmt.Fprintf(&buffer, "C.%s(d.toNative()", f.trampolineName())
	for i, p := range params {
		if i != 0 {
			buffer.WriteString(", ")
			if p.Type == "void" {
				fmt.Fprintf(&buffer, "p%d", i)
			} else if p.Type == "cef_string_t" && p.Ptrs == "*" {
				fmt.Fprintf(&buffer, "&s%d", i)
			} else if p.Type == "char" && p.Ptrs == "**" {
				fmt.Fprintf(&buffer, "(**C.char)(cp%d)", i)
			} else {
				if p.Ptrs == "*" {
					if _, exists := edefsMap[p.Type]; exists {
						fmt.Fprintf(&buffer, "&e%d", i)
						continue
					}
				}
				if sdef, exists := sdefsMap[p.Type]; exists {
					if len(p.Ptrs) > 1 {
						fmt.Fprintf(&buffer, "&pd%d", i)
					} else {
						fmt.Fprintf(&buffer, "p%d.toNative(", i)
						if !sdef.isClassEquivalent() {
							fmt.Fprintf(&buffer, "&C.%s{}", p.Type)
						}
						buffer.WriteString(")")
					}
				} else if len(p.Ptrs) > 0 {
					fmt.Fprintf(&buffer, "(%sC.%s)(p%d)", p.Ptrs, p.Type, i)
				} else {
					fmt.Fprintf(&buffer, "C.%s(p%d)", p.Type, i)
				}
			}
		}
	}
	fmt.Fprintf(&buffer, ", d.%s)", f.Name)
	call := buffer.String()
	buffer.Reset()
	buffer.WriteString(prefixLines)
	if f.GoReturnType == "" {
		buffer.WriteString(call)
	} else {
		if sdef, exists := sdefsMap[f.CReturnType]; exists && !sdef.isClassEquivalent() {
			fmt.Fprintf(&buffer, `native := %s
var result %s
result.fromNative(&native)
return result`, call, f.GoReturnType)
		} else {
			switch f.CReturnType {
			case "cef_string_t":
				fmt.Fprintf(&buffer, "native := %s\nreturn cefstrToString(&native)", call)
			case "cef_string_t *":
				fmt.Fprintf(&buffer, "return cefstrToString(%s)", call)
			case "cef_string_userfree_t":
				fmt.Fprintf(&buffer, "return cefuserfreestrToString(%s)", call)
			default:
				buffer.WriteString("return ")
				if strings.HasPrefix(f.GoReturnType, "*") {
					fmt.Fprintf(&buffer, "(%s)", f.GoReturnType)
				} else {
					buffer.WriteString(f.GoReturnType)
				}
				fmt.Fprintf(&buffer, "(%s)", call)
			}
		}
	}
	return buffer.String()
}

func (f *field) ToNative() string {
	var buffer strings.Builder
	if sdef, exists := sdefsMap[f.CReturnType]; exists && !sdef.isClassEquivalent() {
		fmt.Fprintf(&buffer, "d.%s.toNative(&native.%s)", f.GoName, f.Name)
	} else {
		switch f.CReturnType {
		case "void *":
			fmt.Fprintf(&buffer, "native.%s = d.%s", f.Name, f.GoName)
		case "cef_string_t":
			fmt.Fprintf(&buffer, `setCEFStr(d.%s, &native.%s)`, f.GoName, f.Name)
		case "cef_string_t *":
			fmt.Fprintf(&buffer, `setCEFStr(d.%s, native.%s)`, f.GoName, f.Name)
		default:
			fmt.Fprintf(&buffer, "native.%s = ", f.Name)
			if i := strings.Index(f.CReturnType, " "); i != -1 {
				fmt.Fprintf(&buffer, "(%sC.%s)", f.CReturnType[i+1:], f.CReturnType[:i])
			} else {
				fmt.Fprintf(&buffer, "C.%s", f.CReturnType)
			}
			fmt.Fprintf(&buffer, "(d.%s)", f.GoName)
		}
	}
	return buffer.String()
}

func (f *field) FromNative() string {
	var buffer strings.Builder
	if sdef, exists := sdefsMap[f.CReturnType]; exists && !sdef.isClassEquivalent() {
		fmt.Fprintf(&buffer, "d.%s.fromNative(&native.%s)", f.GoName, f.Name)
	} else {
		fmt.Fprintf(&buffer, "d.%s = ", f.GoName)
		switch f.CReturnType {
		case "cef_string_t":
			fmt.Fprintf(&buffer, "cefstrToString(&native.%s)", f.Name)
		case "cef_string_t *":
			fmt.Fprintf(&buffer, "cefstrToString(native.%s)", f.Name)
		default:
			if strings.HasPrefix(f.GoReturnType, "*") {
				fmt.Fprintf(&buffer, "(%s)", f.GoReturnType)
			} else {
				buffer.WriteString(f.GoReturnType)
			}
			fmt.Fprintf(&buffer, "(native.%s)", f.Name)
		}
	}
	return buffer.String()
}

func (f *field) trampolineName() string {
	return fmt.Sprintf("gocef_%s_%s", strings.TrimSuffix(strings.TrimPrefix(f.Owner.Name, "cef_"), "_t"), f.Name)
}

func (f *field) Trampoline() string {
	if !f.FunctionPtr {
		return ""
	}
	var buffer strings.Builder
	fmt.Fprintf(&buffer, "%s %s(", f.CReturnType, f.trampolineName())
	for i, p := range f.CParams {
		if i == 0 {
			fmt.Fprintf(&buffer, "%s self", p)
		} else {
			fmt.Fprintf(&buffer, ", %s p%d", p, i)
		}
	}
	fmt.Fprintf(&buffer, ", %s (CEF_CALLBACK *callback)(", f.CReturnType)
	for i, p := range f.CParams {
		if i != 0 {
			buffer.WriteString(", ")
		}
		fmt.Fprintf(&buffer, "%s", p)
	}
	buffer.WriteString(")) { return callback(")
	for i := range f.CParams {
		if i == 0 {
			buffer.WriteString("self")
		} else {
			fmt.Fprintf(&buffer, ", p%d", i)
		}
	}
	buffer.WriteString("); }")
	return buffer.String()
}