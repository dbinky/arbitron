package modelidentity

// KnownFamilies lists every vendor family the identity table can prove, in
// table order. A fresh slice is returned on each call.
func KnownFamilies() []Family {
	out := make([]Family, 0, len(families))
	for _, f := range families {
		out = append(out, Family(f.lab))
	}
	return out
}
