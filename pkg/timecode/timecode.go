// Package timecode converts absolute frame numbers to SMPTE 24-hour timecodes
// and expands the %-specifier text patterns that render them.
//
// It is the shared basis for the %hh/%mm/%ss/%ff overlay specifiers and for the
// Time Code SEI (payload type 136) clock timestamps, so a burned-in overlay and
// the SEI of the same picture always agree.
package timecode

import "fmt"

// Components converts an absolute frame index to a 24-hour wall-clock timecode
// (hours 0-23, minutes, seconds, and the frame index within the second),
// wrapping at 24h.
//
// rate is the integer timecode counting rate (frames per timecode-second, e.g.
// 25, 30, 60); for fractional capture rates use the nominal rounded rate
// (29.97 -> 30, 59.94 -> 60). When dropFrame is true, NTSC drop-frame counting
// is applied (valid only for rate 30 and 60): two (rate 30) or four (rate 60)
// timecode labels are skipped at the start of every minute except every tenth,
// keeping the timecode close to real elapsed time. dropped reports whether a
// label drop occurs at this frame (for the SEI cnt_dropped_flag). Negative or
// out-of-range frame indices wrap into [0, framesPerDay). rate <= 0 is treated
// as 1.
func Components(frame int64, rate int, dropFrame bool) (h, m, s, f int, dropped bool) {
	if rate <= 0 {
		rate = 1
	}
	r := int64(rate)

	if !dropFrame {
		framesPerDay := r * 86400
		fd := ((frame % framesPerDay) + framesPerDay) % framesPerDay
		f = int(fd % r)
		sec := fd / r
		s = int(sec % 60)
		m = int((sec / 60) % 60)
		h = int((sec / 3600) % 24)
		return h, m, s, f, false
	}

	// Drop-frame counting (NTSC family): skip `drop` labels at the start of
	// every minute except every tenth minute.
	drop := int64(2)
	if rate == 60 {
		drop = 4
	}
	framesPer10Min := r*600 - 9*drop
	framesPerDay := framesPer10Min * 6 * 24
	fd := ((frame % framesPerDay) + framesPerDay) % framesPerDay

	tenMin := fd / framesPer10Min
	rem := fd % framesPer10Min

	framesPerMin := r*60 - drop
	var minuteInBlock, frameInMin int64
	if rem < r*60 { // first minute of the ten-minute block keeps all labels
		minuteInBlock = 0
		frameInMin = rem
	} else {
		rem -= r * 60
		minuteInBlock = 1 + rem/framesPerMin
		frameInMin = rem % framesPerMin
	}

	label := frameInMin
	if minuteInBlock != 0 {
		label += drop // these minutes drop their first `drop` labels
	}
	f = int(label % r)
	sec := label/r + (tenMin*10+minuteInBlock)*60
	s = int(sec % 60)
	m = int((sec / 60) % 60)
	h = int((sec / 3600) % 24)
	dropped = minuteInBlock != 0 && frameInMin == 0
	return h, m, s, f, dropped
}

// FormatText expands %-specifiers in pattern for the given frame number, using
// the timecode counting rate and drop-frame mode. The %hh/%mm/%ss/%ff/%ms
// timecode specifiers wrap at 24h and honour drop-frame counting (rate 30/60);
// the %d frame-number specifiers use the absolute frame value unchanged.
//
// Supported specifiers:
//
//	%d       frame number (no padding)
//	%Nd      frame number space-padded to N digits (e.g. %3d)
//	%0Nd     frame number zero-padded to N digits (e.g. %03d)
//	%hh      hours (2 digits)
//	%mm      minutes (2 digits)
//	%ss      seconds (2 digits)
//	%ff      frame within current second (2 digits)
//	%ms      milliseconds (3 digits)
//	%%       literal %
//
// An unrecognised specifier passes its % through literally.
func FormatText(pattern string, frameNum, rate int, dropFrame bool) string {
	if rate <= 0 {
		rate = 1
	}

	hours, minutes, seconds, frameInSecond, _ := Components(int64(frameNum), rate, dropFrame)
	milliseconds := frameInSecond * 1000 / rate

	var out []byte
	i := 0
	for i < len(pattern) {
		if pattern[i] != '%' {
			out = append(out, pattern[i])
			i++
			continue
		}

		rest := pattern[i+1:]

		// Two-character named specifiers.
		if len(rest) >= 2 {
			switch rest[:2] {
			case "hh":
				out = append(out, fmt.Sprintf("%02d", hours)...)
				i += 3
				continue
			case "mm":
				out = append(out, fmt.Sprintf("%02d", minutes)...)
				i += 3
				continue
			case "ss":
				out = append(out, fmt.Sprintf("%02d", seconds)...)
				i += 3
				continue
			case "ff":
				out = append(out, fmt.Sprintf("%02d", frameInSecond)...)
				i += 3
				continue
			case "ms":
				out = append(out, fmt.Sprintf("%03d", milliseconds)...)
				i += 3
				continue
			}
		}

		// Literal %.
		if len(rest) >= 1 && rest[0] == '%' {
			out = append(out, '%')
			i += 2
			continue
		}

		// %0Nd (zero-padded), %Nd (space-padded) or %d (no padding).
		if len(rest) >= 1 {
			j := 0
			zeroPad := false
			pad := 0
			if j < len(rest) && rest[j] == '0' {
				zeroPad = true
				j++
			}
			for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
				pad = pad*10 + int(rest[j]-'0')
				j++
			}
			if j < len(rest) && rest[j] == 'd' {
				switch {
				case pad > 0 && zeroPad:
					out = append(out, fmt.Sprintf("%0*d", pad, frameNum)...)
				case pad > 0:
					out = append(out, fmt.Sprintf("%*d", pad, frameNum)...)
				default:
					out = append(out, fmt.Sprintf("%d", frameNum)...)
				}
				i += 1 + j + 1 // '%' + consumed chars + 'd'
				continue
			}
		}

		// Unknown specifier: pass the % through literally.
		out = append(out, '%')
		i++
	}

	return string(out)
}
