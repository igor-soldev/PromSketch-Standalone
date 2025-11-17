package promsketch

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/zzylol/go-kll"
	"github.com/zzylol/prometheus-sketch-VLDB/prometheus-sketches/model/labels"
	"github.com/zzylol/prometheus-sketch-VLDB/prometheus-sketches/util/annotations"
)

const (
	// DefaultStripeSize is the default number of entries to allocate in the stripeSeries hash map.
	DefaultStripeSize = 1 << 14
)

type SketchType int

const (
	SHUniv SketchType = iota + 1
	EHUniv
	EHCount
	EHKLL
	EHDD
	EffSum
	EffSum2
	USampling
)

var funcSketchMap = map[string]([]SketchType){
	"avg_over_time":      {USampling},
	"count_over_time":    {USampling},
	"entropy_over_time":  {EHUniv},
	"max_over_time":      {EHKLL},
	"min_over_time":      {EHKLL},
	"stddev_over_time":   {USampling},
	"stdvar_over_time":   {USampling},
	"sum_over_time":      {USampling},
	"sum2_over_time":     {USampling},
	"distinct_over_time": {EHUniv},
	"l1_over_time":       {EHUniv}, // same as count_over_time
	"l2_over_time":       {EHUniv},
	"quantile_over_time": {EHKLL},
}

// SketchConfig bundles sketch configurations for promsketch
type SketchConfig struct {
	// 	CM_config       CMConfig
	// CS_config      CSConfig
	// Univ_config    UnivConfig
	// SH_univ_config SHUnivConfig
	// 	SH_count_config SHCountConfig
	// EH_count_config EHCountConfig
	EH_univ_config EHUnivConfig
	EH_kll_config  EHKLLConfig
	// EH_dd_config    EHDDConfig
	// EffSum_config   EffSumConfig
	// EffSum2_config  EffSum2Config
	Sampling_config SamplingConfig
}

type CMConfig struct {
	Row_no int
	Col_no int
}

type CSConfig struct {
	Row_no int
	Col_no int
}

type UnivConfig struct {
	TopK_size int
	Row_no    int
	Col_no    int
	Layer     int
}

type SHUnivConfig struct {
	Beta             float64
	Time_window_size int64
	Univ_config      UnivConfig
}

type SHCountConfig struct {
	Beta             float64
	Time_window_size int64
}

type EHCountConfig struct {
	K                int64
	Time_window_size int64
}

type EHUnivConfig struct {
	K                int64
	Time_window_size int64
	Univ_config      UnivConfig
}

type EHKLLConfig struct {
	K                int64
	Kll_k            int
	Time_window_size int64
}

type EHDDConfig struct {
	K                int64
	Time_window_size int64
	DDAccuracy       float64
}

type SamplingConfig struct {
	Sampling_rate    float64
	Time_window_size int64
	Max_size         int
}

type EffSumConfig struct {
	Item_window_size int64
	Time_window_size int64
	Epsilon          float64
	R                float64
}

type EffSum2Config struct {
	Item_window_size int64
	Time_window_size int64
	Epsilon          float64
	R                float64
}

// Each series maintain their own sketches
type SketchInstances struct {
	// shuniv *SmoothHistogramUnivMon
	ehuniv *ExpoHistogramUnivOptimized
	ehkll  *ExpoHistogramKLL
	// ehdd   *ExpoHistogramDD
	sampling *UniformSampling
}

// TODO: can be more efficient timeseries management?
type memSeries struct {
	id              TSId
	lset            labels.Labels
	sketchInstances *SketchInstances
	oldestTimestamp int64
}

type sketchSeriesHashMap struct {
	unique    map[uint64]*memSeries
	conflicts map[uint64][]*memSeries
}

func (m *sketchSeriesHashMap) get(hash uint64, lset labels.Labels) *memSeries {
	if s, found := m.unique[hash]; found {
		if labels.Equal(s.lset, lset) {
			return s
		}
	}
	for _, s := range m.conflicts[hash] {
		if labels.Equal(s.lset, lset) {
			return s
		}
	}
	return nil
}

func (m *sketchSeriesHashMap) set(hash uint64, s *memSeries) {
	if existing, found := m.unique[hash]; !found || labels.Equal(existing.lset, s.lset) {
		m.unique[hash] = s
		return
	}
	if m.conflicts == nil {
		m.conflicts = make(map[uint64][]*memSeries)
	}
	l := m.conflicts[hash]
	for i, prev := range l {
		if labels.Equal(prev.lset, s.lset) {
			l[i] = s
			return
		}
	}
	m.conflicts[hash] = append(l, s)
}

func (m *sketchSeriesHashMap) del(hash uint64, id TSId) {
	var rem []*memSeries
	unique, found := m.unique[hash]
	switch {
	case !found: // Supplied hash is not stored.
		return
	case unique.id == id:
		conflicts := m.conflicts[hash]
		if len(conflicts) == 0 { // Exactly one series with this hash was stored
			delete(m.unique, hash)
			return
		}
		m.unique[hash] = conflicts[0] // First remaining series goes in 'unique'.
		rem = conflicts[1:]           // Keep the rest.
	default: // The series to delete is somewhere in 'conflicts'. Keep all the ones that don't match.
		for _, s := range m.conflicts[hash] {
			if s.id != id {
				rem = append(rem, s)
			}
		}
	}
	if len(rem) == 0 {
		delete(m.conflicts, hash)
	} else {
		m.conflicts[hash] = rem
	}
}

// sketchSeries holds series by ID and also by hash of their labels.
// ID-based lookups via getByID() are preferred over getByHash() for performance reasons.
type sketchSeries struct {
	size   int
	id     TSId
	hashes []sketchSeriesHashMap
	series []map[TSId]*memSeries
	locks  []stripeLock
}

type PromSketches struct {
	lastSeriesID atomic.Uint64
	numSeries    atomic.Uint64
	series       *sketchSeries
}

func (s *sketchSeries) getByID(id TSId) *memSeries {
	if s.size == 0 {
		return nil
	}
	i := uint64(id) & uint64(s.size-1)

	s.locks[i].RLock()
	series := s.series[i][id]
	s.locks[i].RUnlock()

	return series
}

func (s *sketchSeries) getByHash(hash uint64, lset labels.Labels) *memSeries {
	if s.size == 0 {
		return nil
	}
	i := hash & uint64(s.size-1)
	s.locks[i].RLock()
	series := s.hashes[i].get(hash, lset)
	s.locks[i].RUnlock()

	return series
}

type TSId int

func newSlidingHistorgrams(s *memSeries, stype SketchType, sc *SketchConfig) error {
	if s.sketchInstances == nil {
		s.sketchInstances = &SketchInstances{}
	}

	if stype == USampling && s.sketchInstances.sampling == nil {
		s.sketchInstances.sampling = NewUniformSampling(sc.Sampling_config.Time_window_size, sc.Sampling_config.Sampling_rate, int(sc.Sampling_config.Max_size))
	}

	if stype == EHKLL && s.sketchInstances.ehkll == nil {
		s.sketchInstances.ehkll = ExpoInitKLL(sc.EH_kll_config.K, sc.EH_kll_config.Kll_k, sc.EH_kll_config.Time_window_size)
	}

	if stype == EHUniv && s.sketchInstances.ehuniv == nil {
		s.sketchInstances.ehuniv = ExpoInitUnivOptimized(sc.EH_univ_config.K, sc.EH_univ_config.Time_window_size)
	}

	/*
		if stype == EHDD && s.sketchInstances.ehdd == nil {
			s.sketchInstances.ehdd = ExpoInitDD(sc.EH_dd_config.K, sc.EH_dd_config.Time_window_size, sc.EH_dd_config.DDAccuracy)
		}

		if stype == EHCount && s.sketchInstances.ehc == nil {
			s.sketchInstances.ehc = ExpoInitCount(sc.EH_count_config.K, sc.EH_count_config.Time_window_size)
		}

		if stype == EffSum && s.sketchInstances.EffSum == nil {
			s.sketchInstances.EffSum = NewEfficientSum(sc.EffSum_config.Item_window_size, sc.EffSum_config.Time_window_size, sc.EffSum_config.Epsilon, sc.EffSum_config.R)
		}

		if stype == EffSum2 && s.sketchInstances.EffSum2 == nil {
			s.sketchInstances.EffSum2 = NewEfficientSum(sc.EffSum2_config.Item_window_size, sc.EffSum2_config.Time_window_size, sc.EffSum2_config.Epsilon, sc.EffSum2_config.R)
		}


			if stype == EHUniv && s.sketchInstances.ehuniv == nil {
				s.sketchInstances.ehuniv = ExpoInitUniv(sc.EH_univ_config.K, sc.EH_univ_config.Time_window_size)
			}

			if stype == SHCount && s.sketchInstances.shc == nil {
				s.sketchInstances.shc = SmoothInitCount(sc.SH_count_config.Beta, sc.SH_count_config.Time_window_size)
			}

			if stype == EHKLL && s.sketchInstances.ehkll == nil {
				s.sketchInstances.ehkll = ExpoInitKLL(sc.EH_kll_config.K, sc.EH_kll_config.Kll_k, sc.EH_kll_config.Time_window_size)
			}
	*/
	return nil
}

func NewSketchSeries(stripeSize int) *sketchSeries {
	ss := &sketchSeries{ // TODO: use stripeSeries toreduce lock contention later
		size:   stripeSize,
		id:     0,
		hashes: make([]sketchSeriesHashMap, stripeSize),
		series: make([]map[TSId]*memSeries, stripeSize),
		locks:  make([]stripeLock, stripeSize),
	}

	for i := range ss.series {
		ss.series[i] = map[TSId]*memSeries{}
	}
	for i := range ss.hashes {
		ss.hashes[i] = sketchSeriesHashMap{
			unique:    map[uint64]*memSeries{},
			conflicts: nil,
		}
	}
	return ss
}

func NewPromSketches() *PromSketches {
	ss := NewSketchSeries(DefaultStripeSize)
	ps := &PromSketches{
		series: ss,
	}
	ps.lastSeriesID.Store(0)
	ps.numSeries.Store(0)
	return ps
}

/*
func NewPromSketchesWithConfig(sketchRuleTests []SketchRuleTest, sc *SketchConfig) *PromSketches {
	ss := NewSketchSeries(DefaultStripeSize)
	ps := &PromSketches{
		series: ss,
	}
	ps.lastSeriesID.Store(0)
	ps.numSeries.Store(0)
	for _, t := range sketchRuleTests {
		series, _, _ := ps.getOrCreate(t.lset.Hash(), t.lset)
		for stype, exist := range t.stypemap {
			if exist {
				newSketchInstance(series, stype, sc)
			}
		}
	}
	return ps
}

*/

func newMemSeries(lset labels.Labels, id TSId) *memSeries {
	s := &memSeries{
		lset:            lset,
		id:              id,
		sketchInstances: nil,
		oldestTimestamp: -1,
	}
	return s
}

func newSketchInstance(series *memSeries, stype SketchType, sc *SketchConfig) error {
	return newSlidingHistorgrams(series, stype, sc)
}

func (ps *PromSketches) NewSketchCacheInstance(lset labels.Labels, funcName string, time_window_size int64, item_window_size int64, value_scale float64) error {
	series, _, _ := ps.getOrCreate(lset.Hash(), lset)
	stypes := funcSketchMap[funcName]
	sc := SketchConfig{}

	for _, stype := range stypes {
		switch stype {
		case EHUniv:
			sc.EH_univ_config = EHUnivConfig{K: 50, Time_window_size: time_window_size} ///250 K
		case USampling:
			sc.Sampling_config = SamplingConfig{Sampling_rate: 0.1, Time_window_size: time_window_size, Max_size: int(float64(item_window_size) * 0.1)}
		case EHKLL:
			sc.EH_kll_config = EHKLLConfig{K: 50, Time_window_size: time_window_size, Kll_k: 256}
			/*
				case EHCount:
					sc.EH_count_config = EHCountConfig{K: 100, Time_window_size: time_window_size}
				case EffSum:
					sc.EffSum_config = EffSumConfig{Time_window_size: time_window_size, Item_window_size: item_window_size, Epsilon: 0.01, R: value_scale}
				case EffSum2:
					sc.EffSum2_config = EffSum2Config{Time_window_size: time_window_size, Item_window_size: item_window_size, Epsilon: 0.01, R: value_scale}
				case EHDD:
					sc.EH_dd_config = EHDDConfig{K: 100, Time_window_size: time_window_size, DDAccuracy: 0.01}
			*/
		default:
			fmt.Println("[NewSketchCacheInstance] not supported sketch type")
		}
		err := newSketchInstance(series, stype, &sc)
		if err != nil {
			return err
		}
	}

	return nil
}

// Return min_time and max_time from the sketch based on labels and function.
func (ps *PromSketches) PrintCoverage(lset labels.Labels, funcName string) (int64, int64) {
	series := ps.series.getByHash(lset.Hash(), lset)
	if series == nil {
		return -1, -1
	}
	return coverageForSeries(series, funcName)
}

func (ps *PromSketches) PrintAggregateCoverage(base labels.Labels, funcName, groupLabel string) (int64, int64) {
	matched := ps.collectSeriesByLabels(base, groupLabel)
	if len(matched) == 0 {
		return -1, -1
	}
	aggMin := int64(math.MaxInt64)
	aggMax := int64(math.MinInt64)
	for _, series := range matched {
		minT, maxT := coverageForSeries(series, funcName)
		if minT == -1 || maxT == -1 {
			continue
		}
		if minT < aggMin {
			aggMin = minT
		}
		if maxT > aggMax {
			aggMax = maxT
		}
	}
	if aggMax == int64(math.MinInt64) {
		return -1, -1
	}
	return aggMin, aggMax
}

// EnsureAggregateSketches makes sure every matching series has sketch instances needed by funcName.
func (ps *PromSketches) EnsureAggregateSketches(base labels.Labels, funcName string, timeWindowSize, itemWindowSize int64, valueScale float64, groupLabel string) int {
	matched := ps.collectSeriesByLabels(base, groupLabel)
	ensured := 0
	for _, series := range matched {
		if series == nil {
			continue
		}
		if err := ps.NewSketchCacheInstance(series.lset, funcName, timeWindowSize, itemWindowSize, valueScale); err == nil {
			ensured++
		}
	}
	return ensured
}

func coverageForSeries(series *memSeries, funcName string) (int64, int64) {
	if series == nil || series.sketchInstances == nil {
		return -1, -1
	}
	stypes := funcSketchMap[funcName]
	for _, stype := range stypes {
		switch stype {
		case EHUniv:
			if series.sketchInstances.ehuniv == nil {
				return -1, -1
			}
			return series.sketchInstances.ehuniv.GetMinTime(), series.sketchInstances.ehuniv.GetMaxTime()
		case EHKLL:
			if series.sketchInstances.ehkll == nil {
				return -1, -1
			}
			return series.sketchInstances.ehkll.GetMinTime(), series.sketchInstances.ehkll.GetMaxTime()
		case USampling:
			if series.sketchInstances.sampling == nil {
				return -1, -1
			}
			return series.sketchInstances.sampling.GetMinTime(), series.sketchInstances.sampling.GetMaxTime()
		default:
			return -1, -1
		}
	}
	return -1, -1
}

// Check whether the sketch already covers the requested time range
func (ps *PromSketches) LookUp(lset labels.Labels, funcName string, mint, maxt int64) bool {
	// Find the memSeries by labels.
	series := ps.series.getByHash(lset.Hash(), lset)
	if series == nil {
		// fmt.Println("[lookup] no timeseries")
		return false
	}
	// Determine the sketch types for this function
	stypes := funcSketchMap[funcName]
	for _, stype := range stypes {
		switch stype {
		/*
			case EHCount:
				if series.sketchInstances.ehc == nil {
					// fmt.Println("[lookup] ehc no sketch instance")
					return false
				} else if series.sketchInstances.ehc.Cover(mint, maxt) == false {
					// fmt.Println("[lookup] ehc not covered")
					return false
				}
		*/
		case EHUniv:
			if series.sketchInstances.ehuniv == nil {
				fmt.Println("[lookup] no sketch instance")
				return false
			} else if series.sketchInstances.ehuniv.Cover(mint, maxt) == false {
				fmt.Println("[lookup] not covered")
				return false
			}
		case EHKLL:
			if series.sketchInstances.ehkll == nil {
				return false
			} else if series.sketchInstances.ehkll.Cover(mint, maxt) == false {
				return false
			}
		case USampling:
			if series.sketchInstances.sampling == nil {
				return false
			} else if series.sketchInstances.sampling.Cover(mint, maxt) == false {
				return false
			}
		/*
			case EffSum:
				if series.sketchInstances.EffSum == nil {
					// fmt.Println("[lookup] effsum no sketch instance")
					return false
				} else if series.sketchInstances.EffSum.Cover(mint, maxt) == false {
					// fmt.Println("[lookup] effsum not covered")
					return false
				}
			case EffSum2:
				if series.sketchInstances.EffSum2 == nil {
					// fmt.Println("[lookup] no sketch instance")
					return false
				} else if series.sketchInstances.EffSum2.Cover(mint, maxt) == false {
					// fmt.Println("[lookup] not covered")
					return false
				}
			case EHDD:
				if series.sketchInstances.ehdd == nil {
					// fmt.Println("[lookup] no sketch instance")
					return false
				} else if series.sketchInstances.ehdd.Cover(mint, maxt) == false {
					// fmt.Println("[lookup] not covered")
					return false
				}
		*/
		default:
			return false
		}
	}
	return true
}

func (ps *PromSketches) PrintSampling(lset labels.Labels) {
	series := ps.series.getByHash(lset.Hash(), lset)
	fmt.Println(lset, len(series.sketchInstances.sampling.Arr))
	fmt.Println(series.sketchInstances.sampling.GetMemory(), "KB", unsafe.Sizeof(*series.sketchInstances.sampling))
}

func (ps *PromSketches) PrintEHUniv(lset labels.Labels) {
	series := ps.series.getByHash(lset.Hash(), lset)
	fmt.Println(series.sketchInstances.ehuniv.GetTotalBucketSizes(), "total samples")
	fmt.Println(series.sketchInstances.ehuniv.GetMemoryKB(), "KB", series.sketchInstances.ehuniv.map_count, series.sketchInstances.ehuniv.s_count)
}

// Check coverage like LookUp(), but automatically enlarge time_window_size if [mint, maxt] is not covered.
func (ps *PromSketches) LookUpAndUpdateWindow(lset labels.Labels, funcName string, mint, maxt int64) bool {
	series := ps.series.getByHash(lset.Hash(), lset)
	if series == nil {
		// fmt.Println("[lookup] no timeseries")
		return false
	}
	stypes := funcSketchMap[funcName]

	startt := mint
	if series.oldestTimestamp != -1 && mint < series.oldestTimestamp {
		startt = series.oldestTimestamp
	}

	for _, stype := range stypes {
		switch stype {
		case EHUniv:
			if series.sketchInstances.ehuniv == nil {
				fmt.Println("[lookup] no sketch instance")
				return false
			} else if series.sketchInstances.ehuniv.Cover(startt, maxt) == false {
				// If time_window_size < (maxt - mint), grow it (4x)
				if series.sketchInstances.ehuniv.time_window_size < maxt-mint {
					fmt.Println("covered time range:", series.sketchInstances.ehuniv.GetMinTime(), series.sketchInstances.ehuniv.GetMaxTime())
					series.sketchInstances.ehuniv.UpdateWindow(4 * (maxt - mint))
				}
				return false
			}
		case EHKLL:
			if series.sketchInstances.ehkll == nil {
				return false
			} else if series.sketchInstances.ehkll.Cover(startt, maxt) == false {
				if series.sketchInstances.ehkll.time_window_size < maxt-mint {
					fmt.Println("covered time range:", series.sketchInstances.ehkll.GetMinTime(), series.sketchInstances.ehkll.GetMaxTime())
					series.sketchInstances.ehkll.UpdateWindow(4 * (maxt - mint))
				}
				return false
			}
		case USampling:
			if series.sketchInstances.sampling == nil {
				return false
			} else if series.sketchInstances.sampling.Cover(startt, maxt) == false {
				if series.sketchInstances.sampling.Time_window_size < maxt-mint {
					fmt.Println("covered time range:", series.sketchInstances.sampling.GetMinTime(), series.sketchInstances.sampling.GetMaxTime())
					series.sketchInstances.sampling.UpdateWindow(4 * (maxt - mint))
				}
				return false
			}
		default:
			return false
		}
	}
	return true
}

func matchesBaseLabels(series labels.Labels, base labels.Labels, groupLabel string) bool {
	if groupLabel != "" && series.Get(groupLabel) == "" {
		return false
	}
	for _, lbl := range base {
		if groupLabel != "" && lbl.Name == groupLabel {
			continue
		}
		if series.Get(lbl.Name) != lbl.Value {
			return false
		}
	}
	return true
}

func (ps *PromSketches) collectSeriesByLabels(base labels.Labels, groupLabel string) []*memSeries {
	var matched []*memSeries
	if ps.series == nil || ps.series.size == 0 {
		return matched
	}
	for i := range ps.series.series {
		ps.series.locks[i].RLock()
		for _, s := range ps.series.series[i] {
			if matchesBaseLabels(s.lset, base, groupLabel) {
				matched = append(matched, s)
			}
		}
		ps.series.locks[i].RUnlock()
	}
	return matched
}

func aggregateSamplingStats(series []*memSeries, mint, maxt int64) (sum, sum2, count float64, hasData bool) {
	for _, s := range series {
		if s == nil || s.sketchInstances == nil || s.sketchInstances.sampling == nil {
			continue
		}
		cnt := s.sketchInstances.sampling.QueryCount(mint, maxt)
		if math.IsNaN(cnt) || cnt <= 0 {
			continue
		}
		sumVal := s.sketchInstances.sampling.QuerySum(mint, maxt)
		sum2Val := s.sketchInstances.sampling.QuerySum2(mint, maxt)
		if math.IsNaN(sumVal) || math.IsNaN(sum2Val) {
			continue
		}
		hasData = true
		count += cnt
		sum += sumVal
		sum2 += sum2Val
	}
	return
}

func aggregateUnivStats(series []*memSeries, mint, maxt, cur int64) (aggUniv *UnivSketch, aggMap map[float64]int64, total float64, hasData bool) {
	for _, s := range series {
		if s == nil || s.sketchInstances == nil || s.sketchInstances.ehuniv == nil {
			continue
		}
		univ, m, n, _ := s.sketchInstances.ehuniv.QueryIntervalMergeUniv(mint, maxt, cur)
		if univ == nil && m == nil {
			continue
		}
		hasData = true
		if univ != nil && m == nil {
			if aggUniv == nil {
				aggUniv = univ
			} else {
				aggUniv.MergeWith(univ)
			}
			continue
		}
		if m != nil {
			if aggMap == nil {
				aggMap = make(map[float64]int64)
			}
			for value, cnt := range *m {
				aggMap[value] += cnt
			}
			total += n
		}
	}
	return
}

func aggregateKLL(series []*memSeries, mint, maxt int64) (*kll.Sketch, bool) {
	var merged *kll.Sketch
	for _, s := range series {
		if s == nil || s.sketchInstances == nil || s.sketchInstances.ehkll == nil {
			continue
		}
		sk := s.sketchInstances.ehkll.QueryIntervalMergeKLL(mint, maxt)
		if sk == nil {
			continue
		}
		if merged == nil {
			merged = sk
		} else {
			merged.Merge(sk)
		}
	}
	if merged == nil {
		return nil, false
	}
	return merged, true
}

func (ps *PromSketches) EvalAggregate(funcName string, base labels.Labels, otherArgs float64, mint, maxt, curTime int64, groupLabel string) (Vector, annotations.Annotations) {
	matched := ps.collectSeriesByLabels(base, groupLabel)
	if len(matched) == 0 {
		return nil, nil
	}

	switch funcName {
	case "sum_over_time":
		sum, _, count, ok := aggregateSamplingStats(matched, mint, maxt)
		if !ok {
			return Vector{Sample{T: curTime, F: math.NaN()}}, nil
		}
		_ = count
		return Vector{Sample{T: curTime, F: sum}}, nil
	case "sum2_over_time":
		_, sum2, _, ok := aggregateSamplingStats(matched, mint, maxt)
		if !ok {
			return Vector{Sample{T: curTime, F: math.NaN()}}, nil
		}
		return Vector{Sample{T: curTime, F: sum2}}, nil
	case "count_over_time", "change_over_time":
		_, _, count, ok := aggregateSamplingStats(matched, mint, maxt)
		if !ok {
			return Vector{Sample{T: curTime, F: math.NaN()}}, nil
		}
		return Vector{Sample{T: curTime, F: count}}, nil
	case "avg_over_time":
		sum, _, count, ok := aggregateSamplingStats(matched, mint, maxt)
		if !ok || count == 0 {
			return Vector{Sample{T: curTime, F: math.NaN()}}, nil
		}
		return Vector{Sample{T: curTime, F: sum / count}}, nil
	case "stdvar_over_time":
		sum, sum2, count, ok := aggregateSamplingStats(matched, mint, maxt)
		if !ok || count == 0 {
			return Vector{Sample{T: curTime, F: math.NaN()}}, nil
		}
		mean := sum / count
		variance := (sum2 / count) - (mean * mean)
		return Vector{Sample{T: curTime, F: variance}}, nil
	case "stddev_over_time":
		sum, sum2, count, ok := aggregateSamplingStats(matched, mint, maxt)
		if !ok || count == 0 {
			return Vector{Sample{T: curTime, F: math.NaN()}}, nil
		}
		mean := sum / count
		variance := (sum2 / count) - (mean * mean)
		if variance < 0 {
			variance = 0
		}
		return Vector{Sample{T: curTime, F: math.Sqrt(variance)}}, nil
	case "quantile_over_time":
		merged, ok := aggregateKLL(matched, mint, maxt)
		if !ok || merged == nil {
			return Vector{Sample{T: curTime, F: math.NaN()}}, nil
		}
		cdf := merged.CDF()
		val := cdf.Query(otherArgs)
		return Vector{Sample{T: curTime, F: val}}, nil
	case "min_over_time":
		merged, ok := aggregateKLL(matched, mint, maxt)
		if !ok || merged == nil {
			return Vector{Sample{T: curTime, F: math.NaN()}}, nil
		}
		cdf := merged.CDF()
		return Vector{Sample{T: curTime, F: cdf.Query(0)}}, nil
	case "max_over_time":
		merged, ok := aggregateKLL(matched, mint, maxt)
		if !ok || merged == nil {
			return Vector{Sample{T: curTime, F: math.NaN()}}, nil
		}
		cdf := merged.CDF()
		return Vector{Sample{T: curTime, F: cdf.Query(1)}}, nil
	case "entropy_over_time":
		aggUniv, aggMap, total, ok := aggregateUnivStats(matched, mint, maxt, curTime)
		if !ok {
			return Vector{Sample{T: curTime, F: math.NaN()}}, nil
		}
		if aggMap != nil && total > 0 {
			mCopy := aggMap
			return Vector{Sample{T: curTime, F: calc_entropy_map(&mCopy, total)}}, nil
		}
		if aggUniv != nil {
			return Vector{Sample{T: curTime, F: aggUniv.calcEntropy()}}, nil
		}
		return Vector{Sample{T: curTime, F: math.NaN()}}, nil
	case "distinct_over_time":
		aggUniv, aggMap, _, ok := aggregateUnivStats(matched, mint, maxt, curTime)
		if !ok {
			return Vector{Sample{T: curTime, F: math.NaN()}}, nil
		}
		if aggMap != nil {
			mCopy := aggMap
			return Vector{Sample{T: curTime, F: calc_distinct_map(&mCopy)}}, nil
		}
		if aggUniv != nil {
			return Vector{Sample{T: curTime, F: aggUniv.calcCard()}}, nil
		}
		return Vector{Sample{T: curTime, F: math.NaN()}}, nil
	case "l1_over_time":
		aggUniv, aggMap, _, ok := aggregateUnivStats(matched, mint, maxt, curTime)
		if !ok {
			return Vector{Sample{T: curTime, F: math.NaN()}}, nil
		}
		if aggMap != nil {
			mCopy := aggMap
			return Vector{Sample{T: curTime, F: calc_l1_map(&mCopy)}}, nil
		}
		if aggUniv != nil {
			return Vector{Sample{T: curTime, F: aggUniv.calcL1()}}, nil
		}
		return Vector{Sample{T: curTime, F: math.NaN()}}, nil
	case "l2_over_time":
		aggUniv, aggMap, _, ok := aggregateUnivStats(matched, mint, maxt, curTime)
		if !ok {
			return Vector{Sample{T: curTime, F: math.NaN()}}, nil
		}
		if aggMap != nil {
			mCopy := aggMap
			return Vector{Sample{T: curTime, F: calc_l2_map(&mCopy)}}, nil
		}
		if aggUniv != nil {
			return Vector{Sample{T: curTime, F: aggUniv.calcL2()}}, nil
		}
		return Vector{Sample{T: curTime, F: math.NaN()}}, nil
	default:
		return nil, nil
	}
}

func (ps *PromSketches) Eval(funcName string, lset labels.Labels, otherArgs float64, mint, maxt, cur_time int64) (Vector, annotations.Annotations) {
	sfunc := FunctionCalls[funcName]
	series := ps.series.getByHash(lset.Hash(), lset)
	if series == nil {
		return nil, nil
	}

	fmt.Printf("[EVAL] Function: %s, TimeRange: %d → %d\n", funcName, mint, maxt)
	fmt.Printf("[EVAL] Labels: %s\n", lset.String())

	// Optionally log sketch contents: sample count and min/max time
	if series.sketchInstances.sampling != nil {
		fmt.Printf("[EVAL] Sampling sketch size: %d\n", len(series.sketchInstances.sampling.Arr))
	}

	return sfunc(context.TODO(), series, otherArgs, mint, maxt, cur_time), nil
}

func (ps *PromSketches) getOrCreate(hash uint64, lset labels.Labels) (*memSeries, bool, error) {
	s := ps.series.getByHash(hash, lset)
	if s != nil {
		return s, false, nil
	}

	ps.series.id = ps.series.id + 1
	id := ps.series.id

	series := newMemSeries(lset, id)

	i := hash & uint64(ps.series.size-1)
	ps.series.locks[i].Lock()
	ps.series.hashes[i].set(hash, series)
	ps.series.locks[i].Unlock()

	i = uint64(id) & uint64(ps.series.size-1)
	ps.series.locks[i].Lock()
	ps.series.series[i][id] = series
	ps.series.locks[i].Unlock()

	return series, true, nil
}

// GetSketchInstances returns a pointer to the SketchInstances for a memSeries.
func (s *memSeries) GetSketchInstances() *SketchInstances {
	return s.sketchInstances
}

// GetOrCreateWrapper is the exported helper around the internal getOrCreate.
func (ps *PromSketches) GetOrCreateWrapper(hash uint64, lset labels.Labels) (*memSeries, bool, error) {
	return ps.getOrCreate(hash, lset)
}

// NewSketchInstanceWrapper is the exported helper to create a new sketch on a memSeries.
func (ps *PromSketches) NewSketchInstanceWrapper(series *memSeries, stype SketchType, sc *SketchConfig) error {
	return newSketchInstance(series, stype, sc)
}

// SketchInsertInsertionThroughputTest will be called in Prometheus scrape module, only for worst-case insertion throughput test
// t.(int64) is millisecond level timestamp, based on Prometheus timestamp
// all sketch types are enabled at once on a single time series.
func (ps *PromSketches) SketchInsertInsertionThroughputTest(lset labels.Labels, t int64, val float64) error {
	t_1 := time.Now()
	// find or create memSeries based on the label set
	s, iscreate, _ := ps.getOrCreate(lset.Hash(), lset)
	if s == nil {
		return errors.New("memSeries not found")
	}
	since_1 := time.Since(t_1)

	// this is for test worst case data insertion throughput
	if iscreate {
		// For insertion throughput test, we enable all sketches for one timeseries
		// otherwise, we can create new sketch instance upon queried as a cache, in addition to pre-defined sketch rules

		fmt.Println("getOrCreate=", since_1)
		t_now := time.Now()
		sketch_types := []SketchType{EHUniv, EHCount, EHDD, EffSum}
		sketch_config := SketchConfig{
			// CS_config:       CSConfig{Row_no: 3, Col_no: 4096},
			// Univ_config:     UnivConfig{TopK_size: 5, Row_no: 3, Col_no: 4096, Layer: 16},
			// SH_univ_config:  SHUnivConfig{Beta: 0.1, Time_window_size: 1000000},
			EH_univ_config:  EHUnivConfig{K: 50, Time_window_size: 1000000}, //250 K
			EH_kll_config:   EHKLLConfig{K: 50, Kll_k: 256, Time_window_size: 1000000},
			Sampling_config: SamplingConfig{Sampling_rate: 0.05, Time_window_size: 1000000, Max_size: int(50000)},
			// EH_count_config: EHCountConfig{K: 100, Time_window_size: 100000},
			// EffSum_config:   EffSumConfig{Time_window_size: 100000, Item_window_size: 100000, Epsilon: 0.01, R: 10000},
			// EH_dd_config:    EHDDConfig{K: 100, DDAccuracy: 0.01, Time_window_size: 100000},
		}
		for _, sketch_type := range sketch_types {
			newSketchInstance(s, sketch_type, &sketch_config)
		}
		since := time.Since(t_now)
		fmt.Println("new sketch instance=", since)
	}
	/*
		t_2 := time.Now()
		if s.sketchInstances.EffSum != nil {
			s.sketchInstances.EffSum.Insert(t, val)
		}
		if s.sketchInstances.EffSum2 != nil {
			s.sketchInstances.EffSum2.Insert(t, val)
		}
		since_2 := time.Since(t_2)
		fmt.Println("efficientsum=", (s.sketchInstances.EffSum == nil), since_2)

		t_3 := time.Now()
		if s.sketchInstances.ehc != nil {
			s.sketchInstances.ehc.Update(t, val)
		}
		since_3 := time.Since(t_3)
		fmt.Println("ehc=", (s.sketchInstances.ehc == nil), since_3)
	*/

	t_4 := time.Now()
	if s.sketchInstances.ehuniv != nil {
		// s.MemPartAppend(t, val)
	}
	since_4 := time.Since(t_4)
	fmt.Println("ehuniv=", (s.sketchInstances.ehuniv == nil), since_4)

	t_7 := time.Now()
	if s.sketchInstances.ehkll != nil {
		s.sketchInstances.ehkll.Update(t, val)
	}
	since_7 := time.Since(t_7)
	fmt.Println("ehdd=", (s.sketchInstances.ehkll == nil), since_7)

	return nil
}

// SketchInsertDefinedRules will be called in Prometheus scrape module, with pre-defined sketch rules (hard-coded)
// t.(int64) is millisecond level timestamp, based on Prometheus timestamp
func (ps *PromSketches) SketchInsertDefinedRules(lset labels.Labels, t int64, val float64) error {
	// t_1 := time.Now()
	s, iscreate, _ := ps.getOrCreate(lset.Hash(), lset)
	//since_1 := time.Since(t_1)
	if iscreate {
		//	fmt.Println("getOrCreate=", since_1)
	}

	if s == nil {
		return errors.New("memSeries not found")
	}

	/*
		// t_2 := time.Now()
		if s.sketchInstances.EffSum != nil {
			s.sketchInstances.EffSum.Insert(t, val)
		}

		if s.sketchInstances.EffSum2 != nil {
			s.sketchInstances.EffSum2.Insert(t, val)
		}
		//	since_2 := time.Since(t_2)
		//	fmt.Println("effsum=", (s.sketchInstances.EffSum == nil), since_2)

		//	t_3 := time.Now()
		if s.sketchInstances.ehc != nil {
			s.sketchInstances.ehc.Update(t, val)
		}
		//	since_3 := time.Since(t_3)
		//	fmt.Println("ehc=", (s.sketchInstances.ehc == nil), since_3)
	*/

	//	t_4 := time.Now()
	if s.sketchInstances.ehuniv != nil {
		// s.MemPartAppend(t, val)
		// s.sketchInstances.shuniv.Update(t, strconv.FormatFloat(val, 'f', -1, 64))
	}
	//	since_4 := time.Since(t_4)
	//	fmt.Println("shuniv=", (s.sketchInstances.shuniv == nil), since_4)

	//	t_7 := time.Now()
	if s.sketchInstances.ehkll != nil {
		s.sketchInstances.ehkll.Update(t, val)
	}
	//	since_7 := time.Since(t_7)
	//	fmt.Println("ehdd=", (s.sketchInstances.ehdd == nil), since_7)

	return nil
}

// SketchInsert will be called in Prometheus scrape module, for SketchCache version
// t.(int64) is millisecond level timestamp, based on Prometheus timestamp
func (ps *PromSketches) SketchInsert(lset labels.Labels, t int64, val float64) error {
	// start := time.Now()
	s := ps.series.getByHash(lset.Hash(), lset)
	// elapsed := time.Since(start)
	// fmt.Println("getByHash time:", elapsed)
	if s == nil || s.sketchInstances == nil {
		return nil
	}

	if s.sketchInstances.ehkll != nil {
		if s.oldestTimestamp == -1 {
			s.oldestTimestamp = t
		}
		s.sketchInstances.ehkll.Update(t, val)
	}

	if s.sketchInstances.sampling != nil {
		if s.oldestTimestamp == -1 {
			s.oldestTimestamp = t
		}
		s.sketchInstances.sampling.Insert(t, val)
	}

	if s.sketchInstances.ehuniv != nil {
		if s.oldestTimestamp == -1 {
			s.oldestTimestamp = t
		}
		s.sketchInstances.ehuniv.Update(t, val)
	}

	return nil
}

func (ps *PromSketches) StopBackground() {
	for id := 0; id < int(ps.series.id); id++ {
		series := ps.series.getByID(TSId(id))
		if series == nil {
			continue
		}
		if series.sketchInstances.ehuniv != nil {
			series.sketchInstances.ehuniv.StopBackgroundClean()
		}
	}
}

func (ps *PromSketches) GetTotalMemory() float64 { // KB
	var total_mem float64 = 0
	fmt.Println("total series=", ps.series.id)
	for id := 0; id <= int(ps.series.id); id++ {
		series := ps.series.getByID(TSId(id))
		if series == nil {
			continue
		}
		if series.sketchInstances.ehuniv != nil {
			total_mem += series.sketchInstances.ehuniv.GetMemoryKB()
			fmt.Println("TSid=", id, "mem=", series.sketchInstances.ehuniv.GetMemoryKB()/1024, "MB")
		}
		if series.sketchInstances.ehkll != nil {
			total_mem += series.sketchInstances.ehkll.GetMemory()
			fmt.Println("TSid=", id, "mem=", series.sketchInstances.ehkll.GetMemory()/1024, "MB")
		}
		if series.sketchInstances.sampling != nil {
			total_mem += series.sketchInstances.sampling.GetMemory()
			fmt.Println("TSid=", id, "mem=", series.sketchInstances.sampling.GetMemory()/1024, "MB")
		}
		total_mem += float64(unsafe.Sizeof(TSId(id))) / 1024
		total_mem += float64(unsafe.Sizeof(series.lset)) / 1024
		for idx := 0; idx < len(series.lset); idx++ {
			total_mem += float64(unsafe.Sizeof(series.lset[idx].Name)) / 1024
			total_mem += float64(unsafe.Sizeof(series.lset[idx].Value)) / 1024
		}
	}
	total_mem += float64(unsafe.Sizeof(*ps.series)) / 1024
	total_mem += float64(unsafe.Sizeof(ps.series.hashes)) / 1024
	for _, hm := range ps.series.hashes {
		total_mem += float64(unsafe.Sizeof(hm)) / 1024
	}
	total_mem += float64(unsafe.Sizeof(ps.series.series)) / 1024
	for _, m := range ps.series.series {
		total_mem += float64(unsafe.Sizeof(m) / 1024)
	}
	total_mem += float64(unsafe.Sizeof(ps.series.locks)) / 1024
	for _, lock := range ps.series.locks {
		total_mem += float64(unsafe.Sizeof(lock)) / 1024
	}

	return total_mem
}

func (ps *PromSketches) GetTotalMemoryEHUniv() float64 { // KB
	var total_mem float64 = 0
	fmt.Println("total series=", ps.series.id)
	for id := 0; id <= int(ps.series.id); id++ {
		series := ps.series.getByID(TSId(id))
		if series == nil {
			continue
		}
		if series.sketchInstances.ehuniv != nil {
			total_mem += series.sketchInstances.ehuniv.GetMemoryKB()
			fmt.Println("TSid=", id, "mem=", series.sketchInstances.ehuniv.GetMemoryKB()/1024, "MB")
		}

		total_mem += float64(unsafe.Sizeof(TSId(id))) / 1024
		total_mem += float64(unsafe.Sizeof(series.lset)) / 1024
		for idx := 0; idx < len(series.lset); idx++ {
			total_mem += float64(unsafe.Sizeof(series.lset[idx].Name)) / 1024
			total_mem += float64(unsafe.Sizeof(series.lset[idx].Value)) / 1024
		}
	}
	total_mem += float64(unsafe.Sizeof(*ps.series)) / 1024
	total_mem += float64(unsafe.Sizeof(ps.series.hashes)) / 1024
	for _, hm := range ps.series.hashes {
		total_mem += float64(unsafe.Sizeof(hm)) / 1024
	}
	total_mem += float64(unsafe.Sizeof(ps.series.series)) / 1024
	for _, m := range ps.series.series {
		total_mem += float64(unsafe.Sizeof(m) / 1024)
	}
	total_mem += float64(unsafe.Sizeof(ps.series.locks)) / 1024
	for _, lock := range ps.series.locks {
		total_mem += float64(unsafe.Sizeof(lock)) / 1024
	}

	return total_mem
}
