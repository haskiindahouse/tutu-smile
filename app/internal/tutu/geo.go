package tutu

import (
	"sort"
	"strings"
	"unicode"
)

// Coord is a lon/lat pair (Mapbox order).
type Coord struct {
	Lon float64 `json:"lon"`
	Lat float64 `json:"lat"`
}

// cityCoords is a curated table for the map layer. The MCP geo index uses
// internal ids that are not lon/lat, so the frontend map needs real
// coordinates; this covers the demo cities and the largest RU hubs, with a
// graceful "unknown" for anything else (the row still works, it just isn't
// drawn on the map).
var cityCoords = map[string]Coord{
	"москва":           {37.6173, 55.7558},
	"санкт-петербург":  {30.3351, 59.9343},
	"петербург":        {30.3351, 59.9343},
	"спб":              {30.3351, 59.9343},
	"казань":           {49.1064, 55.7963},
	"нижний новгород":  {44.0059, 56.3269},
	"калуга":           {36.2637, 54.5293},
	"пермь":            {56.2270, 58.0105},
	"екатеринбург":     {60.6122, 56.8389},
	"новосибирск":      {82.9346, 55.0084},
	"сочи":             {39.7303, 43.5855},
	"краснодар":        {38.9769, 45.0355},
	"самара":           {50.1500, 53.1959},
	"уфа":              {55.9721, 54.7388},
	"ростов-на-дону":   {39.7015, 47.2357},
	"воронеж":          {39.1843, 51.6720},
	"волгоград":        {44.5133, 48.7080},
	"саратов":          {46.0154, 51.5924},
	"тула":             {37.6173, 54.1930},
	"ярославль":        {39.8845, 57.6261},
	"владимир":         {40.4070, 56.1290},
	"рязань":           {39.7367, 54.6296},
	"тверь":            {35.9176, 56.8587},
	"челябинск":        {61.4291, 55.1644},
	"омск":             {73.3686, 54.9885},
	"красноярск":       {92.8526, 56.0153},
	"тюмень":           {65.5343, 57.1522},
	"ижевск":           {53.2115, 56.8526},
	"киров":            {49.6601, 58.6035},
	"чебоксары":        {47.2519, 56.1439},
	"пенза":            {45.0000, 53.2007},
	"липецк":           {39.5992, 52.6031},
	"курск":            {36.1926, 51.7373},
	"белгород":         {36.5983, 50.5997},
	"смоленск":         {32.0453, 54.7818},
	"брянск":           {34.3634, 53.2434},
	"орёл":             {36.0625, 52.9685},
	"орел":             {36.0625, 52.9685},
	"мурманск":         {33.0827, 68.9585},
	"архангельск":      {40.5433, 64.5401},
	"калининград":      {20.4522, 54.7104},
	"псков":            {28.3496, 57.8194},
	"великий новгород": {31.2699, 58.5228},
	"иваново":          {40.9739, 57.0004},
	"кострома":         {40.9269, 57.7677},
	"тольятти":         {49.3461, 53.5078},
	"астрахань":        {48.0333, 46.3479},
	"ставрополь":       {41.9734, 45.0445},
	"махачкала":        {47.5047, 42.9849},
	"владивосток":      {131.8855, 43.1155},
	"хабаровск":        {135.0838, 48.4802},
	"иркутск":          {104.2807, 52.2870},
	"томск":            {84.9924, 56.4846},
	"барнаул":          {83.7636, 53.3481},
	"кемерово":         {86.0621, 55.3547},
	"саранск":          {45.1749, 54.1838},
	"оренбург":         {55.0988, 51.7727},
	"сургут":           {73.3962, 61.2540},
	"дивеево":          {43.2451, 55.0405},
	"козельск":         {35.7756, 54.0357}, // Оптина Пустынь рядом
	"звенигород":       {36.8547, 55.7314},
	"дмитров":          {37.5183, 56.3439},
	"сергиев посад":    {38.1306, 56.3153},
	"коломна":          {38.7529, 55.0794},
	"рыбинск":          {38.8584, 58.0485},
	"выборг":           {28.7528, 60.7103},
	"вологда":          {39.8915, 59.2205},
	"геленджик":        {38.0766, 44.5622},
	"анапа":            {37.3239, 44.8951},
	"новороссийск":     {37.7706, 44.7235},
	"суздаль":          {40.4405, 56.4192},
	"петрозаводск":     {34.3469, 61.7849},
	"сортавала":        {30.6906, 61.7031},
	"пятигорск":        {43.0578, 44.0486},
	"кисловодск":       {42.7161, 43.9053},
	"дербент":          {48.2958, 42.0577},
}

// transportHubs are well-connected cities a small town relays through when
// it has no direct long-distance service: the roulette prices the trip from
// the hub and honestly adds the электричка-плечо to it.
var transportHubs = []string{
	"Москва", "Санкт-Петербург", "Казань", "Екатеринбург", "Нижний Новгород",
	"Самара", "Краснодар", "Ростов-на-Дону", "Уфа", "Новосибирск", "Воронеж",
}

// IsHub reports whether the city itself is a long-distance hub.
func IsHub(city string) bool {
	for _, h := range transportHubs {
		if strings.EqualFold(h, strings.TrimSpace(city)) {
			return true
		}
	}
	return false
}

// NearestHub picks the geographically closest hub when the origin's
// coordinates are known, and Москва otherwise (the safest feeder default).
func NearestHub(origin string) string {
	oc, ok := CityCoord(origin)
	if !ok {
		return "Москва"
	}
	best, bestD := "Москва", 1e18
	for _, h := range transportHubs {
		hc, ok := CityCoord(h)
		if !ok {
			continue
		}
		dLon, dLat := oc.Lon-hc.Lon, oc.Lat-hc.Lat
		d := dLon*dLon + dLat*dLat
		if d < bestD {
			best, bestD = h, d
		}
	}
	return best
}

// Cities returns display-cased known city names for input autocompletion.
// Aliases (спб, петербург) are skipped; particles stay lowercase.
func Cities() []string {
	skip := map[string]bool{"спб": true, "петербург": true, "орел": true}
	out := make([]string, 0, len(cityCoords))
	for k := range cityCoords {
		if !skip[k] {
			out = append(out, displayCase(k))
		}
	}
	sort.Strings(out)
	return out
}

var lowerParticles = map[string]bool{"на": true, "в": true, "под": true}

func displayCase(s string) string {
	capWord := func(w string, first bool) string {
		if w == "" {
			return w
		}
		if !first && lowerParticles[w] {
			return w
		}
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		return string(r)
	}
	words := strings.Split(s, " ")
	for i, w := range words {
		parts := strings.Split(w, "-")
		for j, p := range parts {
			parts[j] = capWord(p, i == 0 && j == 0)
		}
		words[i] = strings.Join(parts, "-")
	}
	return strings.Join(words, " ")
}

// CityCoord resolves a city name to coordinates. The bool is false when the
// city isn't in the table (the caller keeps the row, just skips the map pin).
func CityCoord(name string) (Coord, bool) {
	key := normalizeCity(name)
	c, ok := cityCoords[key]
	return c, ok
}

func normalizeCity(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	// Drop station/airport qualifiers that ride along in MCP station strings.
	if i := strings.IndexAny(s, "—("); i > 0 {
		s = strings.TrimSpace(s[:i])
	}
	if i := strings.Index(s, ","); i > 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}
