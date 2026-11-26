package apidoc

import (
	"fmt"
	"sort"
	"strings"
)

// ChangeKind classifies a public API change as breaking or additive.
type ChangeKind string

const (
	ChangeBreaking ChangeKind = "breaking"
	ChangeAdditive ChangeKind = "additive"
)

// Change describes a single structural difference between two API snapshots.
type Change struct {
	Kind    ChangeKind `json:"kind"`
	Symbol  string     `json:"symbol"`
	Message string     `json:"message"`
	Old     string     `json:"old,omitempty"`
	New     string     `json:"new,omitempty"`
}

// DiffResult collects all breaking and additive public API changes.
type DiffResult struct {
	Breaking []Change `json:"breaking"`
	Additive []Change `json:"additive"`
}

// HasBreaking reports whether any breaking change was detected.
func (r DiffResult) HasBreaking() bool { return len(r.Breaking) > 0 }

// Empty reports whether the two API surfaces are structurally identical.
func (r DiffResult) Empty() bool { return len(r.Breaking) == 0 && len(r.Additive) == 0 }

// Diff performs a structural comparison of the public API surfaces of oldDoc
// and newDoc, classifying every difference as Breaking or Additive.
func Diff(oldDoc, newDoc *Doc) DiffResult {
	var res DiffResult
	oldViews := indexModules(FlattenPublic(oldDoc))
	newViews := indexModules(FlattenPublic(newDoc))

	for path, oldMod := range oldViews {
		newMod, ok := newViews[path]
		if !ok {
			if hasPublicContent(oldMod) {
				res.addBreaking(moduleSymbol(path, ""), "removed public module", "", "")
			}
			continue
		}
		diffAliases(path, oldMod.Aliases, newMod.Aliases, &res)
		diffTraits(path, oldMod.Traits, newMod.Traits, &res)
		diffStructs(path, oldMod.Structs, newMod.Structs, &res)
		diffFunctions(path, oldMod.Functions, newMod.Functions, false, &res)
	}

	for path, newMod := range newViews {
		if _, ok := oldViews[path]; !ok && hasPublicContent(newMod) {
			res.addAdditive(moduleSymbol(path, ""), "added public module", "", "")
		}
	}

	sortChanges(res.Breaking)
	sortChanges(res.Additive)
	return res
}

func (r *DiffResult) addBreaking(symbol, msg, oldVal, newVal string) {
	r.Breaking = append(r.Breaking, Change{
		Kind:    ChangeBreaking,
		Symbol:  symbol,
		Message: msg,
		Old:     oldVal,
		New:     newVal,
	})
}

func (r *DiffResult) addAdditive(symbol, msg, oldVal, newVal string) {
	r.Additive = append(r.Additive, Change{
		Kind:    ChangeAdditive,
		Symbol:  symbol,
		Message: msg,
		Old:     oldVal,
		New:     newVal,
	})
}

func indexModules(views []ModuleView) map[string]ModuleView {
	m := make(map[string]ModuleView, len(views))
	for _, v := range views {
		m[v.Path] = v
	}
	return m
}

func moduleSymbol(prefix, name string) string {
	switch {
	case prefix == "" && name == "":
		return "<root>"
	case prefix == "":
		return name
	case name == "":
		return prefix
	default:
		return prefix + "." + name
	}
}

func diffAliases(prefix string, oldList, newList []Alias, res *DiffResult) {
	oldMap := make(map[string]Alias, len(oldList))
	for _, a := range oldList {
		oldMap[a.Name] = a
	}
	newMap := make(map[string]Alias, len(newList))
	for _, a := range newList {
		newMap[a.Name] = a
	}

	for name, oa := range oldMap {
		sym := moduleSymbol(prefix, name)
		na, ok := newMap[name]
		if !ok {
			res.addBreaking(sym, "removed public alias", AliasSignature(oa), "")
			continue
		}
		oldSig := normalizeSig(AliasSignature(oa))
		newSig := normalizeSig(AliasSignature(na))
		if oldSig != newSig {
			res.addBreaking(sym, "changed alias definition", oldSig, newSig)
		}
	}
	for name, na := range newMap {
		if _, ok := oldMap[name]; !ok {
			res.addAdditive(moduleSymbol(prefix, name), "added public alias", "", AliasSignature(na))
		}
	}
}

func diffTraits(prefix string, oldList, newList []Trait, res *DiffResult) {
	oldMap := make(map[string]Trait, len(oldList))
	for _, t := range oldList {
		oldMap[t.Name] = t
	}
	newMap := make(map[string]Trait, len(newList))
	for _, t := range newList {
		newMap[t.Name] = t
	}

	for name, ot := range oldMap {
		sym := moduleSymbol(prefix, name)
		nt, ok := newMap[name]
		if !ok {
			res.addBreaking(sym, "removed public trait", TraitSignature(ot), "")
			continue
		}
		diffParentTraits(sym, ot.ParentTraits, nt.ParentTraits, true, res)
		diffAliases(sym, ot.Aliases, nt.Aliases, res)
		diffFunctions(sym, ot.Functions, nt.Functions, true, res)
	}
	for name, nt := range newMap {
		if _, ok := oldMap[name]; !ok {
			res.addAdditive(moduleSymbol(prefix, name), "added public trait", "", TraitSignature(nt))
		}
	}
}

func diffStructs(prefix string, oldList, newList []Struct, res *DiffResult) {
	oldMap := make(map[string]Struct, len(oldList))
	for _, s := range oldList {
		oldMap[s.Name] = s
	}
	newMap := make(map[string]Struct, len(newList))
	for _, s := range newList {
		newMap[s.Name] = s
	}

	for name, os := range oldMap {
		sym := moduleSymbol(prefix, name)
		ns, ok := newMap[name]
		if !ok {
			res.addBreaking(sym, "removed public struct", StructSignature(os), "")
			continue
		}
		diffParentTraits(sym, os.ParentTraits, ns.ParentTraits, false, res)
		diffParams(sym, os.Parameters, ns.Parameters, res)
		diffFields(sym, os.Fields, ns.Fields, res)
		diffAliases(sym, os.Aliases, ns.Aliases, res)
		diffFunctions(sym, os.Functions, ns.Functions, false, res)
	}
	for name, ns := range newMap {
		if _, ok := oldMap[name]; !ok {
			res.addAdditive(moduleSymbol(prefix, name), "added public struct", "", StructSignature(ns))
		}
	}
}

func diffParentTraits(sym string, oldTraits, newTraits []string, isTrait bool, res *DiffResult) {
	oldSet := make(map[string]bool, len(oldTraits))
	for _, t := range oldTraits {
		oldSet[t] = true
	}
	newSet := make(map[string]bool, len(newTraits))
	for _, t := range newTraits {
		newSet[t] = true
	}
	for t := range oldSet {
		if !newSet[t] {
			res.addBreaking(sym, fmt.Sprintf("removed parent trait %s", t), t, "")
		}
	}
	for t := range newSet {
		if !oldSet[t] {
			if isTrait {
				// Adding a required parent trait to a trait breaks existing implementors.
				res.addBreaking(sym, fmt.Sprintf("added required parent trait %s", t), "", t)
			} else {
				res.addAdditive(sym, fmt.Sprintf("implemented trait %s", t), "", t)
			}
		}
	}
}

func diffFields(structSym string, oldFields, newFields []Field, res *DiffResult) {
	oldMap := make(map[string]Field, len(oldFields))
	for _, f := range oldFields {
		oldMap[f.Name] = f
	}
	newMap := make(map[string]Field, len(newFields))
	for _, f := range newFields {
		newMap[f.Name] = f
	}
	for name, of := range oldMap {
		sym := structSym + "." + name
		nf, ok := newMap[name]
		if !ok {
			res.addBreaking(sym, "removed public field", of.Type, "")
			continue
		}
		if strings.TrimSpace(of.Type) != strings.TrimSpace(nf.Type) {
			res.addBreaking(sym, "changed field type", of.Type, nf.Type)
		}
	}
	for name, nf := range newMap {
		if _, ok := oldMap[name]; !ok {
			res.addAdditive(structSym+"."+name, "added public field", "", nf.Type)
		}
	}
}

func diffFunctions(prefix string, oldList, newList []Function, onTrait bool, res *DiffResult) {
	oldMap := make(map[string]Function, len(oldList))
	for _, f := range oldList {
		oldMap[f.Name] = f
	}
	newMap := make(map[string]Function, len(newList))
	for _, f := range newList {
		newMap[f.Name] = f
	}

	for name, of := range oldMap {
		sym := moduleSymbol(prefix, name)
		nf, ok := newMap[name]
		if !ok {
			oldSig := ""
			if len(of.Overloads) > 0 {
				oldSig = OverloadSignature(name, of.Overloads[0])
			}
			res.addBreaking(sym, "removed public function", oldSig, "")
			continue
		}
		diffOverloads(sym, name, of.Overloads, nf.Overloads, onTrait, res)
	}
	for name, nf := range newMap {
		if _, ok := oldMap[name]; !ok {
			newSig := ""
			if len(nf.Overloads) > 0 {
				newSig = OverloadSignature(name, nf.Overloads[0])
			}
			if onTrait {
				res.addBreaking(moduleSymbol(prefix, name), "added required trait method", "", newSig)
			} else {
				res.addAdditive(moduleSymbol(prefix, name), "added public function", "", newSig)
			}
		}
	}
}

func diffOverloads(sym, fnName string, oldOvs, newOvs []Overload, onTrait bool, res *DiffResult) {
	if len(oldOvs) == 1 && len(newOvs) == 1 {
		diffSingleOverload(sym, fnName, oldOvs[0], newOvs[0], onTrait, res)
		return
	}

	oldBySig := make(map[string]Overload, len(oldOvs))
	for _, ov := range oldOvs {
		oldBySig[normalizeSig(OverloadSignature(fnName, ov))] = ov
	}
	newBySig := make(map[string]Overload, len(newOvs))
	for _, ov := range newOvs {
		newBySig[normalizeSig(OverloadSignature(fnName, ov))] = ov
	}

	for sig := range oldBySig {
		if _, ok := newBySig[sig]; !ok {
			res.addBreaking(sym, "removed or changed overload", sig, "")
		}
	}
	for sig := range newBySig {
		if _, ok := oldBySig[sig]; !ok {
			if onTrait {
				res.addBreaking(sym, "added required trait method overload", "", sig)
			} else {
				res.addAdditive(sym, "added overload", "", sig)
			}
		}
	}
}

func diffSingleOverload(sym, fnName string, oldOv, newOv Overload, onTrait bool, res *DiffResult) {
	oldSig := normalizeSig(OverloadSignature(fnName, oldOv))
	newSig := normalizeSig(OverloadSignature(fnName, newOv))
	if oldSig == newSig && oldOv.Raises == newOv.Raises && oldOv.Async == newOv.Async {
		return
	}

	beforeBreaking := len(res.Breaking)
	beforeAdditive := len(res.Additive)

	if oldOv.Async != newOv.Async {
		res.addBreaking(sym, "changed async qualifier", oldSig, newSig)
	}
	if !oldOv.Raises && newOv.Raises {
		res.addBreaking(sym, "function now raises", oldSig, newSig)
	} else if oldOv.Raises && !newOv.Raises {
		if onTrait {
			res.addBreaking(sym, "changed raises qualifier on trait method", oldSig, newSig)
		} else {
			res.addAdditive(sym, "function no longer raises", oldSig, newSig)
		}
	}

	oldRet := returnType(oldOv)
	newRet := returnType(newOv)
	if oldRet != newRet {
		res.addBreaking(sym, "changed return type", oldRet, newRet)
	}

	diffParams(sym, oldOv.Parameters, newOv.Parameters, res)
	diffArgs(sym, oldOv.Args, newOv.Args, onTrait, res)

	// Fallback if structured fields did not explain a signature change (e.g.
	// fixtures providing only `signature` without `args`/`returns`).
	if len(res.Breaking) == beforeBreaking && len(res.Additive) == beforeAdditive && oldSig != newSig {
		res.addBreaking(sym, "changed function signature", oldSig, newSig)
	}
}

func diffArgs(sym string, oldArgs, newArgs []Arg, onTrait bool, res *DiffResult) {
	minLen := len(oldArgs)
	if len(newArgs) < minLen {
		minLen = len(newArgs)
	}
	for i := 0; i < minLen; i++ {
		oa, na := oldArgs[i], newArgs[i]
		if strings.TrimSpace(oa.Type) != strings.TrimSpace(na.Type) {
			res.addBreaking(sym, fmt.Sprintf("changed type of argument %q", oa.Name), oa.Type, na.Type)
		}
		if normalizeConvention(oa.Convention) != normalizeConvention(na.Convention) {
			res.addBreaking(sym, fmt.Sprintf("changed convention of argument %q", oa.Name), oa.Convention, na.Convention)
		}
		if oa.Default != "" && na.Default == "" {
			res.addBreaking(sym, fmt.Sprintf("argument %q is now required", oa.Name), oa.Default, "")
		} else if oa.Default == "" && na.Default != "" && !onTrait {
			res.addAdditive(sym, fmt.Sprintf("added default value for argument %q", na.Name), "", na.Default)
		}
	}
	for i := minLen; i < len(oldArgs); i++ {
		res.addBreaking(sym, fmt.Sprintf("removed argument %q", oldArgs[i].Name), oldArgs[i].Type, "")
	}
	for i := minLen; i < len(newArgs); i++ {
		na := newArgs[i]
		if na.Default == "" || onTrait {
			res.addBreaking(sym, fmt.Sprintf("added required argument %q", na.Name), "", na.Type)
		} else {
			res.addAdditive(sym, fmt.Sprintf("added optional argument %q", na.Name), "", na.Type)
		}
	}
}

func diffParams(sym string, oldParams, newParams []Param, res *DiffResult) {
	minLen := len(oldParams)
	if len(newParams) < minLen {
		minLen = len(newParams)
	}
	for i := 0; i < minLen; i++ {
		op, np := oldParams[i], newParams[i]
		if strings.TrimSpace(op.Type) != strings.TrimSpace(np.Type) {
			res.addBreaking(sym, fmt.Sprintf("changed type of parameter %q", op.Name), op.Type, np.Type)
		}
		if op.Default != "" && np.Default == "" {
			res.addBreaking(sym, fmt.Sprintf("parameter %q is now required", op.Name), op.Default, "")
		} else if op.Default == "" && np.Default != "" {
			res.addAdditive(sym, fmt.Sprintf("added default value for parameter %q", np.Name), "", np.Default)
		}
	}
	for i := minLen; i < len(oldParams); i++ {
		res.addBreaking(sym, fmt.Sprintf("removed parameter %q", oldParams[i].Name), oldParams[i].Type, "")
	}
	for i := minLen; i < len(newParams); i++ {
		np := newParams[i]
		if np.Default == "" {
			res.addBreaking(sym, fmt.Sprintf("added required parameter %q", np.Name), "", np.Type)
		} else {
			res.addAdditive(sym, fmt.Sprintf("added optional parameter %q", np.Name), "", np.Type)
		}
	}
}

func returnType(ov Overload) string {
	if ov.Returns == nil {
		return ""
	}
	return strings.TrimSpace(ov.Returns.Type)
}

func normalizeConvention(c string) string {
	c = strings.TrimSpace(c)
	if c == "" || c == "imm" || c == "read" {
		return "read"
	}
	return c
}

func normalizeSig(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func sortChanges(in []Change) {
	sort.Slice(in, func(i, j int) bool {
		if in[i].Symbol != in[j].Symbol {
			return in[i].Symbol < in[j].Symbol
		}
		if in[i].Message != in[j].Message {
			return in[i].Message < in[j].Message
		}
		return in[i].Old < in[j].Old
	})
}
