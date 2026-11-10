package service

import (
	"context"
	"time"
)

// ProxyScoreGrade is S / A / B / C / D or empty string for no data.
type ProxyScoreGrade string

const (
	ProxyScoreGradeS      ProxyScoreGrade = "S"
	ProxyScoreGradeA      ProxyScoreGrade = "A"
	ProxyScoreGradeB      ProxyScoreGrade = "B"
	ProxyScoreGradeC      ProxyScoreGrade = "C"
	ProxyScoreGradeD      ProxyScoreGrade = "D"
	ProxyScoreGradeNoData ProxyScoreGrade = ""
)

// ProxyScoreAdvice is the renewal recommendation.
type ProxyScoreAdvice string

const (
	ProxyScoreAdviceRenew   ProxyScoreAdvice = "renew"
	ProxyScoreAdviceObserve ProxyScoreAdvice = "observe"
	ProxyScoreAdviceDrop    ProxyScoreAdvice = "drop"
)

// ProxyScoreRow is one proxy's scoring result.
type ProxyScoreRow struct {
	ID         int64            `json:"id"`
	Name       string           `json:"name"`
	Status     string           `json:"status"`
	ExpiresAt  *time.Time       `json:"expires_at"`
	Samples    int64            `json:"samples"`
	P50MS      *int64           `json:"p50_ms"`
	MeanMS     *int64           `json:"mean_ms"`
	P80MS      *int64           `json:"p80_ms"`
	JudgedSlow int64            `json:"judged_slow"`
	Grade      ProxyScoreGrade  `json:"grade"`
	Advice     ProxyScoreAdvice `json:"advice"`
}

// ProxyScoreResult is the full response.
type ProxyScoreResult struct {
	Range string          `json:"range"` // "7d" or "30d"
	Rows  []ProxyScoreRow `json:"rows"`
}

// ProxyScoreQueryRow is what the repository returns for each proxy.
type ProxyScoreQueryRow struct {
	ID         int64
	Name       string
	Status     string
	ExpiresAt  *time.Time
	Samples    int64
	P50MS      *int64
	MeanMS     *int64
	P80MS      *int64
	JudgedSlow int64
}

// ProxyScoreRepository is the data access contract.
type ProxyScoreRepository interface {
	QueryScores(ctx context.Context, since time.Time) ([]ProxyScoreQueryRow, error)
}

// ProxyScoreService computes proxy grades.
type ProxyScoreService struct {
	repo ProxyScoreRepository
	now  func() time.Time
}

// NewProxyScoreService creates a new ProxyScoreService.
func NewProxyScoreService(repo ProxyScoreRepository) *ProxyScoreService {
	return &ProxyScoreService{repo: repo, now: time.Now}
}

// Get returns scored rows for the given range ("7d" or "30d").
func (s *ProxyScoreService) Get(ctx context.Context, rangeStr string) (*ProxyScoreResult, error) {
	var since time.Time
	switch rangeStr {
	case "30d":
		since = s.now().Add(-30 * 24 * time.Hour)
	default:
		rangeStr = "7d"
		since = s.now().Add(-7 * 24 * time.Hour)
	}

	rows, err := s.repo.QueryScores(ctx, since)
	if err != nil {
		return nil, err
	}

	out := make([]ProxyScoreRow, 0, len(rows))
	for _, r := range rows {
		grade := proxyScoreGrade(r.P50MS)
		if r.JudgedSlow >= 3 {
			grade = proxyScoreDemote(grade)
		}
		out = append(out, ProxyScoreRow{
			ID:         r.ID,
			Name:       r.Name,
			Status:     r.Status,
			ExpiresAt:  r.ExpiresAt,
			Samples:    r.Samples,
			P50MS:      r.P50MS,
			MeanMS:     r.MeanMS,
			P80MS:      r.P80MS,
			JudgedSlow: r.JudgedSlow,
			Grade:      grade,
			Advice:     proxyScoreAdvice(grade),
		})
	}
	return &ProxyScoreResult{Range: rangeStr, Rows: out}, nil
}

// proxyScoreGrade maps p50 (ms) to a base grade. nil p50 → no-data.
func proxyScoreGrade(p50 *int64) ProxyScoreGrade {
	if p50 == nil {
		return ProxyScoreGradeNoData
	}
	switch {
	case *p50 < 6000:
		return ProxyScoreGradeS
	case *p50 < 10000:
		return ProxyScoreGradeA
	case *p50 < 15000:
		return ProxyScoreGradeB
	case *p50 < 20000:
		return ProxyScoreGradeC
	default:
		return ProxyScoreGradeD
	}
}

// proxyScoreDemote drops a grade by one step.
func proxyScoreDemote(g ProxyScoreGrade) ProxyScoreGrade {
	switch g {
	case ProxyScoreGradeS:
		return ProxyScoreGradeA
	case ProxyScoreGradeA:
		return ProxyScoreGradeB
	case ProxyScoreGradeB:
		return ProxyScoreGradeC
	case ProxyScoreGradeC, ProxyScoreGradeD:
		return ProxyScoreGradeD
	default:
		return g
	}
}

// proxyScoreAdvice maps a grade to a renewal recommendation.
func proxyScoreAdvice(g ProxyScoreGrade) ProxyScoreAdvice {
	switch g {
	case ProxyScoreGradeS, ProxyScoreGradeA:
		return ProxyScoreAdviceRenew
	case ProxyScoreGradeB:
		return ProxyScoreAdviceObserve
	default:
		return ProxyScoreAdviceDrop
	}
}
