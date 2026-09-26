package backend

// Select chooses the gaming backend: Wolf first (verified socket API),
// Sunshine host-process second. It probes in order and returns the first
// REAL backend; otherwise an UNAVAILABLE verdict naming every cause.
// It never returns a MOCK and never claims a backend works without
// contacting it.
func Select() (GamingBackend, Availability) {
	wb := NewWolfBackend()
	wolfAvail := wb.Probe()
	if wolfAvail.Mode == ModeReal {
		return wb, wolfAvail
	}
	sb := NewSunshineBackend()
	sunAvail := sb.Probe()
	if sunAvail.Mode == ModeReal {
		return sb, sunAvail
	}
	return nil, Availability{Mode: ModeUnavailable,
		Reason: "no gaming backend available (wolf: " + wolfAvail.Reason +
			"; sunshine: " + sunAvail.Reason + ") — see docs/operations/wolf.md"}
}
