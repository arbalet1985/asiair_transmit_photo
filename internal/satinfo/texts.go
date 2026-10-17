package satinfo

import "fmt"

// The codes of the CelesTrak satellite catalogue in words
// (https://celestrak.org/satcat/satcat-format.php).

var typeTexts = map[string]string{
	"PAY": "спутник (полезная нагрузка)",
	"R/B": "ступень ракеты",
	"DEB": "обломок",
	"UNK": "неизвестный объект",
}

var statusTexts = map[string]string{
	"+": "работает",
	"-": "не работает",
	"P": "работает частично",
	"B": "резерв",
	"S": "запасной",
	"X": "продлённая миссия",
	"D": "сошёл с орбиты",
	"?": "неизвестно",
}

// ownerTexts: the owners most often met; the others are shown by code.
var ownerTexts = map[string]string{
	"US":   "США",
	"CIS":  "СССР / Россия",
	"PRC":  "Китай",
	"UK":   "Великобритания",
	"FR":   "Франция",
	"GER":  "Германия",
	"IT":   "Италия",
	"JPN":  "Япония",
	"IND":  "Индия",
	"CA":   "Канада",
	"ESA":  "Европейское космическое агентство",
	"EUME": "EUMETSAT",
	"EUTE": "Eutelsat",
	"ITSO": "Intelsat",
	"SES":  "SES",
	"GLOB": "Globalstar",
	"IRID": "Iridium",
	"ORB":  "Orbcomm",
	"O3B":  "O3b",
	"ISS":  "МКС (несколько стран)",
	"SKOR": "Южная Корея",
	"NKOR": "КНДР",
	"ISRA": "Израиль",
	"IRAN": "Иран",
	"BRAZ": "Бразилия",
	"AUS":  "Австралия",
	"SPN":  "Испания",
	"NETH": "Нидерланды",
	"TURK": "Турция",
	"UAE":  "ОАЭ",
	"SAUD": "Саудовская Аравия",
	"ARGN": "Аргентина",
	"TWN":  "Тайвань",
	"FIN":  "Финляндия",
	"LUXE": "Люксембург",
	"NZ":   "Новая Зеландия",
	"KAZ":  "Казахстан",
	"UKR":  "Украина",
	"BEL":  "Беларусь",
	"SING": "Сингапур",
	"INDO": "Индонезия",
	"MEX":  "Мексика",
	"NOR":  "Норвегия",
	"SWED": "Швеция",
	"SWTZ": "Швейцария",
	"POL":  "Польша",
	"CZE":  "Чехия",
	"TBD":  "не определён",
}

// siteTexts: the launch sites most often met.
var siteTexts = map[string]string{
	"AFETR": "мыс Канаверал (США)",
	"AFWTR": "Ванденберг (США)",
	"TYMSC": "Байконур",
	"TTMTR": "Байконур",
	"PKMTR": "Плесецк",
	"VOSTO": "Восточный",
	"KYMSC": "Капустин Яр",
	"JSC":   "Цзюцюань (Китай)",
	"TAISC": "Тайюань (Китай)",
	"XICLF": "Сичан (Китай)",
	"WSC":   "Вэньчан (Китай)",
	"YSLA":  "Хайян, морской старт (Китай)",
	"FRGUI": "Куру (Французская Гвиана)",
	"SRILR": "Шрихарикота (Индия)",
	"TANSC": "Танэгасима (Япония)",
	"KSCUT": "Утиноура (Япония)",
	"WLPIS": "Уоллопс (США)",
	"RLLB":  "Rocket Lab, Новая Зеландия",
	"KWAJ":  "Кваджалейн",
	"SEAL":  "Морской старт",
	"SVOBO": "Свободный",
	"DLS":   "Ясный (Домбаровский)",
	"NSC":   "Наро (Южная Корея)",
	"SMTS":  "Семнан (Иран)",
	"YAVNE": "Пальмахим (Израиль)",
	"SNMLP": "Сан-Марко (Кения)",
}

// texts fills the words of the codes.
func (i *Info) texts() {
	i.TypeText = typeTexts[i.Type]
	i.StatusText = statusTexts[i.Status]
	if t, ok := ownerTexts[i.Owner]; ok {
		i.OwnerText = t
	} else {
		i.OwnerText = i.Owner
	}
	if t, ok := siteTexts[i.Site]; ok {
		i.SiteText = t
	} else {
		i.SiteText = i.Site
	}
}

// Lines: what is known, line by line: a name and its value.
func (i *Info) Lines() [][2]string {
	var out [][2]string
	add := func(k, v string) {
		if v != "" {
			out = append(out, [2]string{k, v})
		}
	}
	id := fmt.Sprintf("NORAD %d", i.NORAD)
	if i.Intl != "" {
		id += ", " + i.Intl
	}
	add("Номер", id)
	t := i.TypeText
	if i.StatusText != "" && i.Type == "PAY" {
		t += ", " + i.StatusText
	}
	add("Что это", t)
	add("Владелец", i.OwnerText)
	add("Оператор", i.Operator)
	if i.Launch != "" {
		l := i.Launch
		if i.SiteText != "" {
			l += ", " + i.SiteText
		}
		add("Запуск", l)
	}
	if i.Decay != "" {
		add("Сошёл с орбиты", i.Decay)
	}
	if i.Apogee > 0 || i.Perigee > 0 {
		add("Орбита", fmt.Sprintf("%.0f × %.0f км, наклонение %.1f°, период %.1f мин", i.Perigee, i.Apogee, i.Incl, i.Period))
	}
	if i.RCS > 0 {
		add("Эффективная площадь рассеяния", fmt.Sprintf("%.2f м²", i.RCS))
	}
	return out
}

// Credit: where the picture is from, in one line.
func (p *Photo) Credit() string {
	t := p.Source
	if p.Author != "" {
		t += ", " + p.Author
	}
	if p.License != "" {
		t += ", " + p.License
	}
	if p.OfType != "" {
		t = "Спутник того же типа (" + p.OfType + "), не этот. " + t
	}
	return t
}
