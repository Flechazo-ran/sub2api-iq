package repository

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestInsertSystemMetricsNullableIntegerMetrics(t *testing.T) {
	createdAt := time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)
	zero := 0

	tests := []struct {
		name         string
		dbConnActive *int
		wantDBActive driver.Value
	}{
		{name: "explicit zero is preserved", dbConnActive: &zero, wantDBActive: int64(0)},
		{name: "unavailable metric remains null", dbConnActive: nil, wantDBActive: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })

			args := make([]driver.Value, 43)
			args[0] = createdAt
			args[1] = int64(1)
			for i := 4; i <= 12; i++ {
				args[i] = int64(0)
			}
			args[35] = tt.wantDBActive

			mock.ExpectExec("INSERT INTO ops_system_metrics").
				WithArgs(args...).
				WillReturnResult(sqlmock.NewResult(1, 1))

			repo := &opsRepository{db: db}
			err = repo.InsertSystemMetrics(context.Background(), &service.OpsInsertSystemMetricsInput{
				CreatedAt:    createdAt,
				DBConnActive: tt.dbConnActive,
			})
			if err != nil {
				t.Fatalf("InsertSystemMetrics: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

func TestInsertSystemMetricsRequestBodyBudget(t *testing.T) {
	createdAt := time.Date(2026, time.November, 12, 12, 0, 0, 0, time.UTC)
	limit, peak, rejected := int64(1500), int64(321), int64(7)

	tests := []struct {
		name  string
		input service.OpsInsertSystemMetricsInput
		want  []driver.Value
	}{
		{
			name:  "budget usage is stored",
			input: service.OpsInsertSystemMetricsInput{CreatedAt: createdAt, RequestBodyBudgetLimitMB: &limit, RequestBodyBudgetPeakMB: &peak, RequestBodyBudgetRejected: &rejected},
			want:  []driver.Value{int64(1500), int64(321), int64(7)},
		},
		{
			name:  "disabled budget stays null",
			input: service.OpsInsertSystemMetricsInput{CreatedAt: createdAt},
			want:  []driver.Value{nil, nil, nil},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })

			args := make([]driver.Value, 43)
			args[0] = createdAt
			args[1] = int64(1)
			for i := 4; i <= 12; i++ {
				args[i] = int64(0)
			}
			copy(args[40:], tt.want)

			mock.ExpectExec("INSERT INTO ops_system_metrics").
				WithArgs(args...).
				WillReturnResult(sqlmock.NewResult(1, 1))

			repo := &opsRepository{db: db}
			input := tt.input
			if err := repo.InsertSystemMetrics(context.Background(), &input); err != nil {
				t.Fatalf("InsertSystemMetrics: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}
