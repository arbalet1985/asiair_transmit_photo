package satellites

// SGP4/SDP4 after Vallado, Crawford, Hujsak and Kelso, "Revisiting
// Spacetrack Report #3" (2006, with the later fixes of the reference code),
// WGS-72 constants and the "improved" operation mode, as the sgp4 package
// of Python (used by Skyfield) computes it.

import (
	"errors"
	"math"
)

const (
	twoPi  = 2 * math.Pi
	x2o3   = 2.0 / 3.0
	earthR = 6378.135 // km, WGS-72
	mu     = 398600.8 // km³/s²
	j2     = 0.001082616
	j3     = -0.00000253881
	j4     = -0.00000165597
	j3oj2  = j3 / j2
)

var xke = 60.0 / math.Sqrt(earthR*earthR*earthR/mu)

// elements is the state of SGP4 for one orbit.
type elements struct {
	// from the TLE
	epoch                                         float64 // days from 1949 December 31 00:00 UT
	bstar, ecco, argpo, inclo, mo, noKozai, nodeo float64

	isimp bool
	deep  bool
	noUnk float64
	aycof, con41, cc1, cc4, cc5, d2, d3, d4, delmo, eta, argpdot, omgcof,
	sinmao, t2cof, t3cof, t4cof, t5cof, x1mth2, x7thm1, mdot, nodedot, xlcof,
	xmcof, nodecf float64
	// deep space
	irez                                                                  int
	d2201, d2211, d3210, d3222, d4410, d4422, d5220, d5232, d5421, d5433  float64
	dedt, del1, del2, del3, didt, dmdt, dnodt, domdt, e3, ee2, peo, pgho  float64
	pho, pinco, plo, se2, se3, sgh2, sgh3, sgh4, sh2, sh3, si2, si3, sl2  float64
	sl3, sl4, gsto, xfact, xgh2, xgh3, xgh4, xh2, xh3, xi2, xi3, xl2, xl3 float64
	xl4, xlamo, zmol, zmos, atime, xli, xni                               float64
	alta, altp                                                            float64 // apogee, perigee in Earth radii above the surface
}

var (
	errDecayed   = errors.New("orbit decayed")
	errElements  = errors.New("bad orbital elements")
	errEccentric = errors.New("eccentricity out of range")
)

// gstime: Greenwich mean sidereal time (radians) at a UT1 Julian date.
func gstime(jdut1 float64) float64 {
	t := (jdut1 - 2451545.0) / 36525.0
	g := -6.2e-6*t*t*t + 0.093104*t*t + (876600.0*3600+8640184.812866)*t + 67310.54841
	g = math.Mod(g*math.Pi/180/240.0, twoPi)
	if g < 0 {
		g += twoPi
	}
	return g
}

// dpper: the lunar-solar periodics of deep space orbits.
func (s *elements) dpper(t float64, init bool, ep, inclp, nodep, argpp, mp *float64) {
	const (
		zns = 1.19459e-5
		zes = 0.01675
		znl = 1.5835218e-4
		zel = 0.05490
	)
	zm := s.zmos + zns*t
	if init {
		zm = s.zmos
	}
	zf := zm + 2*zes*math.Sin(zm)
	sinzf := math.Sin(zf)
	f2 := 0.5*sinzf*sinzf - 0.25
	f3 := -0.5 * sinzf * math.Cos(zf)
	ses := s.se2*f2 + s.se3*f3
	sis := s.si2*f2 + s.si3*f3
	sls := s.sl2*f2 + s.sl3*f3 + s.sl4*sinzf
	sghs := s.sgh2*f2 + s.sgh3*f3 + s.sgh4*sinzf
	shs := s.sh2*f2 + s.sh3*f3
	zm = s.zmol + znl*t
	if init {
		zm = s.zmol
	}
	zf = zm + 2*zel*math.Sin(zm)
	sinzf = math.Sin(zf)
	f2 = 0.5*sinzf*sinzf - 0.25
	f3 = -0.5 * sinzf * math.Cos(zf)
	sel := s.ee2*f2 + s.e3*f3
	sil := s.xi2*f2 + s.xi3*f3
	sll := s.xl2*f2 + s.xl3*f3 + s.xl4*sinzf
	sghl := s.xgh2*f2 + s.xgh3*f3 + s.xgh4*sinzf
	shll := s.xh2*f2 + s.xh3*f3
	pe := ses + sel
	pinc := sis + sil
	pl := sls + sll
	pgh := sghs + sghl
	ph := shs + shll
	if init {
		return
	}
	pe -= s.peo
	pinc -= s.pinco
	pl -= s.plo
	pgh -= s.pgho
	ph -= s.pho
	*inclp += pinc
	*ep += pe
	sinip, cosip := math.Sin(*inclp), math.Cos(*inclp)
	if *inclp >= 0.2 {
		ph /= sinip
		pgh -= cosip * ph
		*argpp += pgh
		*nodep += ph
		*mp += pl
		return
	}
	// Lyddane modification for low inclinations
	sinop, cosop := math.Sin(*nodep), math.Cos(*nodep)
	alfdp := sinip * sinop
	betdp := sinip * cosop
	dalf := ph*cosop + pinc*cosip*sinop
	dbet := -ph*sinop + pinc*cosip*cosop
	alfdp += dalf
	betdp += dbet
	*nodep = math.Mod(*nodep, twoPi)
	xls := *mp + *argpp + cosip**nodep
	dls := pl + pgh - pinc**nodep*sinip
	xls += dls
	xnoh := *nodep
	*nodep = math.Atan2(alfdp, betdp)
	if math.Abs(xnoh-*nodep) > math.Pi {
		if *nodep < xnoh {
			*nodep += twoPi
		} else {
			*nodep -= twoPi
		}
	}
	*mp += pl
	*argpp = xls - *mp - cosip**nodep
}

// dscomOut: what dscom computes for dsinit and dpper.
type dscomOut struct {
	sinim, cosim, emsq, em, nm                   float64
	s1, s2, s3, s4, s5, ss1, ss2, ss3, ss4, ss5  float64
	sz1, sz3, sz11, sz13, sz21, sz23, sz31, sz33 float64
	z1, z3, z11, z13, z21, z23, z31, z33         float64
}

// dscom: the deep space common terms; it sets the periodics of s.
func (s *elements) dscom(epoch, ep, argpp, tc, inclp, nodep, np float64) dscomOut {
	const (
		zes    = 0.01675
		zel    = 0.05490
		c1ss   = 2.9864797e-6
		c1l    = 4.7968065e-7
		zsinis = 0.39785416
		zcosis = 0.91744867
		zcosgs = 0.1945905
		zsings = -0.98088458
	)
	var o dscomOut
	nm := np
	em := ep
	snodm, cnodm := math.Sin(nodep), math.Cos(nodep)
	sinomm, cosomm := math.Sin(argpp), math.Cos(argpp)
	sinim, cosim := math.Sin(inclp), math.Cos(inclp)
	emsq := em * em
	betasq := 1 - emsq
	rtemsq := math.Sqrt(betasq)

	s.peo, s.pinco, s.plo, s.pgho, s.pho = 0, 0, 0, 0, 0
	day := epoch + 18261.5 + tc/1440.0
	xnodce := math.Mod(4.5236020-9.2422029e-4*day, twoPi)
	stem, ctem := math.Sin(xnodce), math.Cos(xnodce)
	zcosil := 0.91375164 - 0.03568096*ctem
	zsinil := math.Sqrt(1 - zcosil*zcosil)
	zsinhl := 0.089683511 * stem / zsinil
	zcoshl := math.Sqrt(1 - zsinhl*zsinhl)
	gam := 5.8351514 + 0.0019443680*day
	zx := 0.39785416 * stem / zsinil
	zy := zcoshl*ctem + 0.91744867*zsinhl*stem
	zx = math.Atan2(zx, zy)
	zx = gam + zx - xnodce
	zcosgl, zsingl := math.Cos(zx), math.Sin(zx)

	zcosg, zsing := zcosgs, zsings
	zcosi, zsini := zcosis, zsinis
	zcosh, zsinh := cnodm, snodm
	cc := c1ss
	xnoi := 1 / nm

	var s1, s2, s3, s4, s5, s6, s7 float64
	var z1, z2, z3, z11, z12, z13, z21, z22, z23, z31, z32, z33 float64
	var ss1, ss2, ss3, ss4, ss5, ss6, ss7 float64
	var sz1, sz2, sz3, sz11, sz12, sz13, sz21, sz22, sz23, sz31, sz32, sz33 float64
	for lsflg := 1; lsflg <= 2; lsflg++ {
		a1 := zcosg*zcosh + zsing*zcosi*zsinh
		a3 := -zsing*zcosh + zcosg*zcosi*zsinh
		a7 := -zcosg*zsinh + zsing*zcosi*zcosh
		a8 := zsing * zsini
		a9 := zsing*zsinh + zcosg*zcosi*zcosh
		a10 := zcosg * zsini
		a2 := cosim*a7 + sinim*a8
		a4 := cosim*a9 + sinim*a10
		a5 := -sinim*a7 + cosim*a8
		a6 := -sinim*a9 + cosim*a10

		x1 := a1*cosomm + a2*sinomm
		x2 := a3*cosomm + a4*sinomm
		x3 := -a1*sinomm + a2*cosomm
		x4 := -a3*sinomm + a4*cosomm
		x5 := a5 * sinomm
		x6 := a6 * sinomm
		x7 := a5 * cosomm
		x8 := a6 * cosomm

		z31 = 12*x1*x1 - 3*x3*x3
		z32 = 24*x1*x2 - 6*x3*x4
		z33 = 12*x2*x2 - 3*x4*x4
		z1 = 3*(a1*a1+a2*a2) + z31*emsq
		z2 = 6*(a1*a3+a2*a4) + z32*emsq
		z3 = 3*(a3*a3+a4*a4) + z33*emsq
		z11 = -6*a1*a5 + emsq*(-24*x1*x7-6*x3*x5)
		z12 = -6*(a1*a6+a3*a5) + emsq*(-24*(x2*x7+x1*x8)-6*(x3*x6+x4*x5))
		z13 = -6*a3*a6 + emsq*(-24*x2*x8-6*x4*x6)
		z21 = 6*a2*a5 + emsq*(24*x1*x5-6*x3*x7)
		z22 = 6*(a4*a5+a2*a6) + emsq*(24*(x2*x5+x1*x6)-6*(x4*x7+x3*x8))
		z23 = 6*a4*a6 + emsq*(24*x2*x6-6*x4*x8)
		z1 = z1 + z1 + betasq*z31
		z2 = z2 + z2 + betasq*z32
		z3 = z3 + z3 + betasq*z33
		s3 = cc * xnoi
		s2 = -0.5 * s3 / rtemsq
		s4 = s3 * rtemsq
		s1 = -15 * em * s4
		s5 = x1*x3 + x2*x4
		s6 = x2*x3 + x1*x4
		s7 = x2*x4 - x1*x3

		if lsflg == 1 {
			ss1, ss2, ss3, ss4, ss5, ss6, ss7 = s1, s2, s3, s4, s5, s6, s7
			sz1, sz2, sz3 = z1, z2, z3
			sz11, sz12, sz13 = z11, z12, z13
			sz21, sz22, sz23 = z21, z22, z23
			sz31, sz32, sz33 = z31, z32, z33
			zcosg, zsing = zcosgl, zsingl
			zcosi, zsini = zcosil, zsinil
			zcosh = zcoshl*cnodm + zsinhl*snodm
			zsinh = snodm*zcoshl - cnodm*zsinhl
			cc = c1l
		}
	}
	s.zmol = math.Mod(4.7199672+0.22997150*day-gam, twoPi)
	s.zmos = math.Mod(6.2565837+0.017201977*day, twoPi)

	// solar terms
	s.se2 = 2 * ss1 * ss6
	s.se3 = 2 * ss1 * ss7
	s.si2 = 2 * ss2 * sz12
	s.si3 = 2 * ss2 * (sz13 - sz11)
	s.sl2 = -2 * ss3 * sz2
	s.sl3 = -2 * ss3 * (sz3 - sz1)
	s.sl4 = -2 * ss3 * (-21 - 9*emsq) * zes
	s.sgh2 = 2 * ss4 * sz32
	s.sgh3 = 2 * ss4 * (sz33 - sz31)
	s.sgh4 = -18 * ss4 * zes
	s.sh2 = -2 * ss2 * sz22
	s.sh3 = -2 * ss2 * (sz23 - sz21)
	// lunar terms
	s.ee2 = 2 * s1 * s6
	s.e3 = 2 * s1 * s7
	s.xi2 = 2 * s2 * z12
	s.xi3 = 2 * s2 * (z13 - z11)
	s.xl2 = -2 * s3 * z2
	s.xl3 = -2 * s3 * (z3 - z1)
	s.xl4 = -2 * s3 * (-21 - 9*emsq) * zel
	s.xgh2 = 2 * s4 * z32
	s.xgh3 = 2 * s4 * (z33 - z31)
	s.xgh4 = -18 * s4 * zel
	s.xh2 = -2 * s2 * z22
	s.xh3 = -2 * s2 * (z23 - z21)

	o.sinim, o.cosim, o.emsq, o.em, o.nm = sinim, cosim, emsq, em, nm
	o.s1, o.s2, o.s3, o.s4, o.s5 = s1, s2, s3, s4, s5
	o.ss1, o.ss2, o.ss3, o.ss4, o.ss5 = ss1, ss2, ss3, ss4, ss5
	o.sz1, o.sz3, o.sz11, o.sz13, o.sz21, o.sz23, o.sz31, o.sz33 = sz1, sz3, sz11, sz13, sz21, sz23, sz31, sz33
	o.z1, o.z3, o.z11, o.z13, o.z21, o.z23, o.z31, o.z33 = z1, z3, z11, z13, z21, z23, z31, z33
	return o
}

// dsinit: the deep space resonance terms.
func (s *elements) dsinit(c dscomOut, eccsq, xpidot float64) {
	const (
		q22    = 1.7891679e-6
		q31    = 2.1460748e-6
		q33    = 2.2123015e-7
		root22 = 1.7891679e-6
		root44 = 7.3636953e-9
		root54 = 2.1765803e-9
		rptim  = 4.37526908801129966e-3
		root32 = 3.7393792e-7
		root52 = 1.1428639e-7
		znl    = 1.5835218e-4
		zns    = 1.19459e-5
	)
	cosim, sinim, emsq := c.cosim, c.sinim, c.emsq
	em, nm := c.em, c.nm
	inclm := s.inclo
	tc := 0.0

	s.irez = 0
	if nm < 0.0052359877 && nm > 0.0034906585 {
		s.irez = 1
	}
	if nm >= 8.26e-3 && nm <= 9.24e-3 && em >= 0.5 {
		s.irez = 2
	}
	// solar terms
	ses := c.ss1 * zns * c.ss5
	sis := c.ss2 * zns * (c.sz11 + c.sz13)
	sls := -zns * c.ss3 * (c.sz1 + c.sz3 - 14 - 6*emsq)
	sghs := c.ss4 * zns * (c.sz31 + c.sz33 - 6)
	shs := -zns * c.ss2 * (c.sz21 + c.sz23)
	if inclm < 5.2359877e-2 || inclm > math.Pi-5.2359877e-2 {
		shs = 0
	}
	if sinim != 0 {
		shs /= sinim
	}
	sgs := sghs - cosim*shs
	// lunar terms
	s.dedt = ses + c.s1*znl*c.s5
	s.didt = sis + c.s2*znl*(c.z11+c.z13)
	s.dmdt = sls - znl*c.s3*(c.z1+c.z3-14-6*emsq)
	sghl := c.s4 * znl * (c.z31 + c.z33 - 6)
	shll := -znl * c.s2 * (c.z21 + c.z23)
	if inclm < 5.2359877e-2 || inclm > math.Pi-5.2359877e-2 {
		shll = 0
	}
	s.domdt = sgs + sghl
	s.dnodt = shs
	if sinim != 0 {
		s.domdt -= cosim / sinim * shll
		s.dnodt += shll / sinim
	}
	theta := math.Mod(s.gsto+tc*rptim, twoPi)
	if s.irez == 0 {
		return
	}
	aonv := math.Pow(nm/xke, x2o3)
	if s.irez == 2 { // 12 hour resonance
		cosisq := cosim * cosim
		emo := em
		em = s.ecco
		emsqo := emsq
		emsq = eccsq
		eoc := em * emsq
		g201 := -0.306 - (em-0.64)*0.440
		var g211, g310, g322, g410, g422, g520, g521, g532, g533 float64
		if em <= 0.65 {
			g211 = 3.616 - 13.2470*em + 16.2900*emsq
			g310 = -19.302 + 117.3900*em - 228.4190*emsq + 156.5910*eoc
			g322 = -18.9068 + 109.7927*em - 214.6334*emsq + 146.5816*eoc
			g410 = -41.122 + 242.6940*em - 471.0940*emsq + 313.9530*eoc
			g422 = -146.407 + 841.8800*em - 1629.014*emsq + 1083.4350*eoc
			g520 = -532.114 + 3017.977*em - 5740.032*emsq + 3708.2760*eoc
		} else {
			g211 = -72.099 + 331.819*em - 508.738*emsq + 266.724*eoc
			g310 = -346.844 + 1582.851*em - 2415.925*emsq + 1246.113*eoc
			g322 = -342.585 + 1554.908*em - 2366.899*emsq + 1215.972*eoc
			g410 = -1052.797 + 4758.686*em - 7193.992*emsq + 3651.957*eoc
			g422 = -3581.690 + 16178.110*em - 24462.770*emsq + 12422.520*eoc
			if em > 0.715 {
				g520 = -5149.66 + 29936.92*em - 54087.36*emsq + 31324.56*eoc
			} else {
				g520 = 1464.74 - 4664.75*em + 3763.64*emsq
			}
		}
		if em < 0.7 {
			g533 = -919.22770 + 4988.6100*em - 9064.7700*emsq + 5542.21*eoc
			g521 = -822.71072 + 4568.6173*em - 8491.4146*emsq + 5337.524*eoc
			g532 = -853.66600 + 4690.2500*em - 8624.7700*emsq + 5341.4*eoc
		} else {
			g533 = -37995.780 + 161616.52*em - 229838.20*emsq + 109377.94*eoc
			g521 = -51752.104 + 218913.95*em - 309468.16*emsq + 146349.42*eoc
			g532 = -40023.880 + 170470.89*em - 242699.48*emsq + 115605.82*eoc
		}
		sini2 := sinim * sinim
		f220 := 0.75 * (1 + 2*cosim + cosisq)
		f221 := 1.5 * sini2
		f321 := 1.875 * sinim * (1 - 2*cosim - 3*cosisq)
		f322 := -1.875 * sinim * (1 + 2*cosim - 3*cosisq)
		f441 := 35 * sini2 * f220
		f442 := 39.3750 * sini2 * sini2
		f522 := 9.84375 * sinim * (sini2*(1-2*cosim-5*cosisq) + 0.33333333*(-2+4*cosim+6*cosisq))
		f523 := sinim * (4.92187512*sini2*(-2-4*cosim+10*cosisq) + 6.56250012*(1+2*cosim-3*cosisq))
		f542 := 29.53125 * sinim * (2 - 8*cosim + cosisq*(-12+8*cosim+10*cosisq))
		f543 := 29.53125 * sinim * (-2 - 8*cosim + cosisq*(12+8*cosim-10*cosisq))
		xno2 := nm * nm
		ainv2 := aonv * aonv
		temp1 := 3 * xno2 * ainv2
		temp := temp1 * root22
		s.d2201 = temp * f220 * g201
		s.d2211 = temp * f221 * g211
		temp1 *= aonv
		temp = temp1 * root32
		s.d3210 = temp * f321 * g310
		s.d3222 = temp * f322 * g322
		temp1 *= aonv
		temp = 2 * temp1 * root44
		s.d4410 = temp * f441 * g410
		s.d4422 = temp * f442 * g422
		temp1 *= aonv
		temp = temp1 * root52
		s.d5220 = temp * f522 * g520
		s.d5232 = temp * f523 * g532
		temp = 2 * temp1 * root54
		s.d5421 = temp * f542 * g521
		s.d5433 = temp * f543 * g533
		s.xlamo = math.Mod(s.mo+s.nodeo+s.nodeo-theta-theta, twoPi)
		s.xfact = s.mdot + s.dmdt + 2*(s.nodedot+s.dnodt-rptim) - s.noUnk
		em = emo
		emsq = emsqo
	}
	if s.irez == 1 { // synchronous resonance
		g200 := 1 + emsq*(-2.5+0.8125*emsq)
		g310 := 1 + 2*emsq
		g300 := 1 + emsq*(-6+6.60937*emsq)
		f220 := 0.75 * (1 + cosim) * (1 + cosim)
		f311 := 0.9375*sinim*sinim*(1+3*cosim) - 0.75*(1+cosim)
		f330 := 1 + cosim
		f330 = 1.875 * f330 * f330 * f330
		s.del1 = 3 * nm * nm * aonv * aonv
		s.del2 = 2 * s.del1 * f220 * g200 * q22
		s.del3 = 3 * s.del1 * f330 * g300 * q33 * aonv
		s.del1 = s.del1 * f311 * g310 * q31 * aonv
		s.xlamo = math.Mod(s.mo+s.nodeo+s.argpo-theta, twoPi)
		s.xfact = s.mdot + xpidot - rptim + s.dmdt + s.domdt + s.dnodt - s.noUnk
	}
	s.xli = s.xlamo
	s.xni = s.noUnk
	s.atime = 0
}

// dspace: the deep space secular effects and resonance at time t (minutes).
func (s *elements) dspace(t float64, em, argpm, inclm, mm, nodem, nm *float64) {
	const (
		fasx2 = 0.13130908
		fasx4 = 2.8843198
		fasx6 = 0.37448087
		g22   = 5.7686396
		g32   = 0.95240898
		g44   = 1.8014998
		g52   = 1.0508330
		g54   = 4.4108898
		rptim = 4.37526908801129966e-3
		stepp = 720.0
		stepn = -720.0
		step2 = 259200.0
	)
	tc := t
	theta := math.Mod(s.gsto+tc*rptim, twoPi)
	*em += s.dedt * t
	*inclm += s.didt * t
	*argpm += s.domdt * t
	*nodem += s.dnodt * t
	*mm += s.dmdt * t
	if s.irez == 0 {
		return
	}
	if s.atime == 0 || t*s.atime <= 0 || math.Abs(t) < math.Abs(s.atime) {
		s.atime = 0
		s.xni = s.noUnk
		s.xli = s.xlamo
	}
	delt := stepn
	if t > 0 {
		delt = stepp
	}
	var xndt, xldot, xnddt, ft float64
	for {
		if s.irez != 2 {
			xndt = s.del1*math.Sin(s.xli-fasx2) + s.del2*math.Sin(2*(s.xli-fasx4)) + s.del3*math.Sin(3*(s.xli-fasx6))
			xldot = s.xni + s.xfact
			xnddt = s.del1*math.Cos(s.xli-fasx2) + 2*s.del2*math.Cos(2*(s.xli-fasx4)) + 3*s.del3*math.Cos(3*(s.xli-fasx6))
			xnddt *= xldot
		} else {
			xomi := s.argpo + s.argpdot*s.atime
			x2omi := xomi + xomi
			x2li := s.xli + s.xli
			xndt = s.d2201*math.Sin(x2omi+s.xli-g22) + s.d2211*math.Sin(s.xli-g22) +
				s.d3210*math.Sin(xomi+s.xli-g32) + s.d3222*math.Sin(-xomi+s.xli-g32) +
				s.d4410*math.Sin(x2omi+x2li-g44) + s.d4422*math.Sin(x2li-g44) +
				s.d5220*math.Sin(xomi+s.xli-g52) + s.d5232*math.Sin(-xomi+s.xli-g52) +
				s.d5421*math.Sin(xomi+x2li-g54) + s.d5433*math.Sin(-xomi+x2li-g54)
			xldot = s.xni + s.xfact
			xnddt = s.d2201*math.Cos(x2omi+s.xli-g22) + s.d2211*math.Cos(s.xli-g22) +
				s.d3210*math.Cos(xomi+s.xli-g32) + s.d3222*math.Cos(-xomi+s.xli-g32) +
				s.d5220*math.Cos(xomi+s.xli-g52) + s.d5232*math.Cos(-xomi+s.xli-g52) +
				2*(s.d4410*math.Cos(x2omi+x2li-g44)+s.d4422*math.Cos(x2li-g44)+
					s.d5421*math.Cos(xomi+x2li-g54)+s.d5433*math.Cos(-xomi+x2li-g54))
			xnddt *= xldot
		}
		if math.Abs(t-s.atime) < stepp {
			ft = t - s.atime
			break
		}
		s.xli += xldot*delt + xndt*step2
		s.xni += xndt*delt + xnddt*step2
		s.atime += delt
	}
	*nm = s.xni + xndt*ft + xnddt*ft*ft*0.5
	xl := s.xli + xldot*ft + xndt*ft*ft*0.5
	if s.irez != 1 {
		*mm = xl - 2**nodem + 2*theta
	} else {
		*mm = xl - *nodem - *argpm + theta
	}
	dndt := *nm - s.noUnk
	*nm = s.noUnk + dndt
}

// init prepares the propagation from the elements read from a TLE.
func (s *elements) init() error {
	const temp4 = 1.5e-12
	ss := 78.0/earthR + 1
	qzms2t := math.Pow((120.0-78.0)/earthR, 4)

	// initl
	eccsq := s.ecco * s.ecco
	omeosq := 1 - eccsq
	rteosq := math.Sqrt(omeosq)
	cosio := math.Cos(s.inclo)
	cosio2 := cosio * cosio
	ak := math.Pow(xke/s.noKozai, x2o3)
	d1 := 0.75 * j2 * (3*cosio2 - 1) / (rteosq * omeosq)
	del := d1 / (ak * ak)
	adel := ak * (1 - del*del - del*(1.0/3.0+134*del*del/81))
	del = d1 / (adel * adel)
	s.noUnk = s.noKozai / (1 + del)
	ao := math.Pow(xke/s.noUnk, x2o3)
	sinio := math.Sin(s.inclo)
	po := ao * omeosq
	con42 := 1 - 5*cosio2
	s.con41 = -con42 - cosio2 - cosio2
	posq := po * po
	rp := ao * (1 - s.ecco)
	s.gsto = gstime(s.epoch + 2433281.5)

	a := math.Pow(s.noUnk/xke, -x2o3) // semi-major axis in Earth radii
	s.alta = a*(1+s.ecco) - 1
	s.altp = a*(1-s.ecco) - 1
	if !(omeosq >= 0 || s.noUnk >= 0) {
		return errElements
	}
	s.isimp = rp < 220.0/earthR+1
	sfour := ss
	qzms24 := qzms2t
	perige := (rp - 1) * earthR
	if perige < 156 {
		sfour = perige - 78
		if perige < 98 {
			sfour = 20
		}
		qzms24 = math.Pow((120-sfour)/earthR, 4)
		sfour = sfour/earthR + 1
	}
	pinvsq := 1 / posq
	tsi := 1 / (ao - sfour)
	s.eta = ao * s.ecco * tsi
	etasq := s.eta * s.eta
	eeta := s.ecco * s.eta
	psisq := math.Abs(1 - etasq)
	coef := qzms24 * math.Pow(tsi, 4)
	coef1 := coef / math.Pow(psisq, 3.5)
	cc2 := coef1 * s.noUnk * (ao*(1+1.5*etasq+eeta*(4+etasq)) +
		0.375*j2*tsi/psisq*s.con41*(8+3*etasq*(8+etasq)))
	s.cc1 = s.bstar * cc2
	cc3 := 0.0
	if s.ecco > 1e-4 {
		cc3 = -2 * coef * tsi * j3oj2 * s.noUnk * sinio / s.ecco
	}
	s.x1mth2 = 1 - cosio2
	s.cc4 = 2 * s.noUnk * coef1 * ao * omeosq * (s.eta*(2+0.5*etasq) + s.ecco*(0.5+2*etasq) -
		j2*tsi/(ao*psisq)*(-3*s.con41*(1-2*eeta+etasq*(1.5-0.5*eeta))+
			0.75*s.x1mth2*(2*etasq-eeta*(1+etasq))*math.Cos(2*s.argpo)))
	s.cc5 = 2 * coef1 * ao * omeosq * (1 + 2.75*(etasq+eeta) + eeta*etasq)
	cosio4 := cosio2 * cosio2
	temp1 := 1.5 * j2 * pinvsq * s.noUnk
	temp2 := 0.5 * temp1 * j2 * pinvsq
	temp3 := -0.46875 * j4 * pinvsq * pinvsq * s.noUnk
	s.mdot = s.noUnk + 0.5*temp1*rteosq*s.con41 + 0.0625*temp2*rteosq*(13-78*cosio2+137*cosio4)
	s.argpdot = -0.5*temp1*con42 + 0.0625*temp2*(7-114*cosio2+395*cosio4) + temp3*(3-36*cosio2+49*cosio4)
	xhdot1 := -temp1 * cosio
	s.nodedot = xhdot1 + (0.5*temp2*(4-19*cosio2)+2*temp3*(3-7*cosio2))*cosio
	xpidot := s.argpdot + s.nodedot
	s.omgcof = s.bstar * cc3 * math.Cos(s.argpo)
	s.xmcof = 0
	if s.ecco > 1e-4 {
		s.xmcof = -x2o3 * coef * s.bstar / eeta
	}
	s.nodecf = 3.5 * omeosq * xhdot1 * s.cc1
	s.t2cof = 1.5 * s.cc1
	if math.Abs(cosio+1) > 1.5e-12 {
		s.xlcof = -0.25 * j3oj2 * sinio * (3 + 5*cosio) / (1 + cosio)
	} else {
		s.xlcof = -0.25 * j3oj2 * sinio * (3 + 5*cosio) / temp4
	}
	s.aycof = -0.5 * j3oj2 * sinio
	s.delmo = math.Pow(1+s.eta*math.Cos(s.mo), 3)
	s.sinmao = math.Sin(s.mo)
	s.x7thm1 = 7*cosio2 - 1

	if twoPi/s.noUnk >= 225 { // deep space
		s.deep = true
		s.isimp = true
		c := s.dscom(s.epoch, s.ecco, s.argpo, 0, s.inclo, s.nodeo, s.noUnk)
		ep, inclp, nodep, argpp, mp := s.ecco, s.inclo, s.nodeo, s.argpo, s.mo
		s.dpper(0, true, &ep, &inclp, &nodep, &argpp, &mp)
		s.ecco, s.inclo, s.nodeo, s.argpo, s.mo = ep, inclp, nodep, argpp, mp
		s.dsinit(c, eccsq, xpidot)
	}
	if !s.isimp {
		cc1sq := s.cc1 * s.cc1
		s.d2 = 4 * ao * tsi * cc1sq
		temp := s.d2 * tsi * s.cc1 / 3
		s.d3 = (17*ao + sfour) * temp
		s.d4 = 0.5 * temp * ao * tsi * (221*ao + 31*sfour) * s.cc1
		s.t3cof = s.d2 + 2*cc1sq
		s.t4cof = 0.25 * (3*s.d3 + s.cc1*(12*s.d2+10*cc1sq))
		s.t5cof = 0.2 * (3*s.d4 + 12*s.cc1*s.d3 + 6*s.d2*s.d2 + 15*cc1sq*(2*s.d2+cc1sq))
	}
	_, err := s.propagate(0)
	return err
}

// propagate returns the position (km, TEME) tsince minutes after the epoch.
// It changes the resonance state of deep space orbits: not for concurrent use.
func (s *elements) propagate(tsince float64) ([3]float64, error) {
	const temp4 = 1.5e-12
	var r [3]float64
	t := tsince
	xmdf := s.mo + s.mdot*t
	argpdf := s.argpo + s.argpdot*t
	nodedf := s.nodeo + s.nodedot*t
	argpm := argpdf
	mm := xmdf
	t2 := t * t
	nodem := nodedf + s.nodecf*t2
	tempa := 1 - s.cc1*t
	tempe := s.bstar * s.cc4 * t
	templ := s.t2cof * t2
	if !s.isimp {
		delomg := s.omgcof * t
		delmtemp := 1 + s.eta*math.Cos(xmdf)
		delm := s.xmcof * (delmtemp*delmtemp*delmtemp - s.delmo)
		temp := delomg + delm
		mm = xmdf + temp
		argpm = argpdf - temp
		t3 := t2 * t
		t4 := t3 * t
		tempa = tempa - s.d2*t2 - s.d3*t3 - s.d4*t4
		tempe += s.bstar * s.cc5 * (math.Sin(mm) - s.sinmao)
		templ += s.t3cof*t3 + t4*(s.t4cof+t*s.t5cof)
	}
	nm := s.noUnk
	em := s.ecco
	inclm := s.inclo
	if s.deep {
		s.dspace(t, &em, &argpm, &inclm, &mm, &nodem, &nm)
	}
	if nm <= 0 {
		return r, errElements
	}
	am := math.Pow(xke/nm, x2o3) * tempa * tempa
	nm = xke / math.Pow(am, 1.5)
	em -= tempe
	if em >= 1 || em < -0.001 {
		return r, errEccentric
	}
	if em < 1e-6 {
		em = 1e-6
	}
	mm += s.noUnk * templ
	xlm := mm + argpm + nodem
	nodem = math.Mod(nodem, twoPi)
	argpm = math.Mod(argpm, twoPi)
	xlm = math.Mod(xlm, twoPi)
	mm = math.Mod(xlm-argpm-nodem, twoPi)

	sinim, cosim := math.Sin(inclm), math.Cos(inclm)
	ep, xincp, argpp, nodep, mp := em, inclm, argpm, nodem, mm
	sinip, cosip := sinim, cosim
	aycof, xlcof := s.aycof, s.xlcof
	con41, x1mth2, x7thm1 := s.con41, s.x1mth2, s.x7thm1
	if s.deep {
		s.dpper(t, false, &ep, &xincp, &nodep, &argpp, &mp)
		if xincp < 0 {
			xincp = -xincp
			nodep += math.Pi
			argpp -= math.Pi
		}
		if ep < 0 || ep > 1 {
			return r, errEccentric
		}
		sinip, cosip = math.Sin(xincp), math.Cos(xincp)
		aycof = -0.5 * j3oj2 * sinip
		if math.Abs(cosip+1) > 1.5e-12 {
			xlcof = -0.25 * j3oj2 * sinip * (3 + 5*cosip) / (1 + cosip)
		} else {
			xlcof = -0.25 * j3oj2 * sinip * (3 + 5*cosip) / temp4
		}
	}
	// long period periodics
	axnl := ep * math.Cos(argpp)
	temp := 1 / (am * (1 - ep*ep))
	aynl := ep*math.Sin(argpp) + temp*aycof
	xl := mp + argpp + nodep + temp*xlcof*axnl
	// Kepler's equation
	u := math.Mod(xl-nodep, twoPi)
	eo1 := u
	tem5 := 9999.9
	var sineo1, coseo1 float64
	for ktr := 1; math.Abs(tem5) >= 1e-12 && ktr <= 10; ktr++ {
		sineo1, coseo1 = math.Sin(eo1), math.Cos(eo1)
		tem5 = 1 - coseo1*axnl - sineo1*aynl
		tem5 = (u - aynl*coseo1 + axnl*sineo1 - eo1) / tem5
		if math.Abs(tem5) >= 0.95 {
			if tem5 > 0 {
				tem5 = 0.95
			} else {
				tem5 = -0.95
			}
		}
		eo1 += tem5
	}
	// short period preliminary quantities
	ecose := axnl*coseo1 + aynl*sineo1
	esine := axnl*sineo1 - aynl*coseo1
	el2 := axnl*axnl + aynl*aynl
	pl := am * (1 - el2)
	if pl < 0 {
		return r, errElements
	}
	rl := am * (1 - ecose)
	betal := math.Sqrt(1 - el2)
	temp = esine / (1 + betal)
	sinu := am / rl * (sineo1 - aynl - axnl*temp)
	cosu := am / rl * (coseo1 - axnl + aynl*temp)
	su := math.Atan2(sinu, cosu)
	sin2u := (cosu + cosu) * sinu
	cos2u := 1 - 2*sinu*sinu
	temp = 1 / pl
	temp1 := 0.5 * j2 * temp
	temp2 := temp1 * temp
	if s.deep {
		cosisq := cosip * cosip
		con41 = 3*cosisq - 1
		x1mth2 = 1 - cosisq
		x7thm1 = 7*cosisq - 1
	}
	mrt := rl*(1-1.5*temp2*betal*con41) + 0.5*temp1*x1mth2*cos2u
	su -= 0.25 * temp2 * x7thm1 * sin2u
	xnode := nodep + 1.5*temp2*cosip*sin2u
	xinc := xincp + 1.5*temp2*cosip*sinip*cos2u
	sinsu, cossu := math.Sin(su), math.Cos(su)
	snod, cnod := math.Sin(xnode), math.Cos(xnode)
	sini, cosi := math.Sin(xinc), math.Cos(xinc)
	xmx := -snod * cosi
	xmy := cnod * cosi
	ux := xmx*sinsu + cnod*cossu
	uy := xmy*sinsu + snod*cossu
	uz := sini * sinsu
	r = [3]float64{mrt * ux * earthR, mrt * uy * earthR, mrt * uz * earthR}
	if mrt < 1 {
		return r, errDecayed
	}
	return r, nil
}
