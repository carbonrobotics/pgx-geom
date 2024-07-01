package pgxgeom_test

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxtest"
	"github.com/twpayne/go-geom"
	"github.com/twpayne/go-geom/encoding/ewkb"
	"github.com/twpayne/go-geom/encoding/wkt"

	pgxgeom "github.com/twpayne/pgx-geom"
)

var defaultConnTestRunner pgxtest.ConnTestRunner

func init() {
	defaultConnTestRunner = pgxtest.DefaultConnTestRunner()
	defaultConnTestRunner.AfterConnect = func(ctx context.Context, tb testing.TB, conn *pgx.Conn) {
		tb.Helper()
		_, err := conn.Exec(ctx, "create extension if not exists postgis")
		assert.NoError(tb, err)
		assert.NoError(tb, pgxgeom.Register(ctx, conn))
	}
}

func TestCodecDecodeValue(t *testing.T) {
	defaultConnTestRunner.RunTest(context.Background(), t, func(ctx context.Context, tb testing.TB, conn *pgx.Conn) {
		tb.Helper()
		for _, format := range []int16{
			pgx.BinaryFormatCode,
			pgx.TextFormatCode,
		} {
			tb.(*testing.T).Run(strconv.Itoa(int(format)), func(t *testing.T) {
				original := mustNewGeomFromWKT(t, "POINT(1 2)", 4326)
				rows, err := conn.Query(ctx, "select $1::geometry", pgx.QueryResultFormats{format}, original)
				assert.NoError(t, err)

				for rows.Next() {
					values, err := rows.Values()
					assert.NoError(t, err)

					assert.Equal(t, 1, len(values))
					v0, ok := values[0].(geom.T)
					assert.True(t, ok)
					assert.Equal(t, mustEWKB(t, original), mustEWKB(t, v0))
				}

				assert.NoError(t, rows.Err())
			})
		}
	})
}

func TestCodecDecodeNullValue(t *testing.T) {
	defaultConnTestRunner.RunTest(context.Background(), t, func(ctx context.Context, tb testing.TB, conn *pgx.Conn) {
		tb.Helper()

		type s struct {
			Geom geom.T `db:"geom"`
		}

		for _, format := range []int16{
			pgx.BinaryFormatCode,
			pgx.TextFormatCode,
		} {
			tb.(*testing.T).Run(strconv.Itoa(int(format)), func(t *testing.T) {
				tb.Helper()

				rows, err := conn.Query(ctx, "select NULL::geometry AS geom", pgx.QueryResultFormats{format})
				assert.NoError(t, err)

				value, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[s])
				assert.NoError(t, err)
				assert.Zero(t, value)
			})
		}
	})
}

func TestCodecDecodeNullValuePolymorphic(t *testing.T) {
	defaultConnTestRunner.RunTest(context.Background(), t, func(ctx context.Context, tb testing.TB, conn *pgx.Conn) {
		tb.Helper()

		type s struct {
			Geom *geom.Point `db:"geom"`
		}

		for _, format := range []int16{
			pgx.BinaryFormatCode,
			pgx.TextFormatCode,
		} {
			tb.(*testing.T).Run(strconv.Itoa(int(format)), func(t *testing.T) {
				tb.Helper()

				rows, err := conn.Query(ctx, "select NULL::geometry AS geom", pgx.QueryResultFormats{format})
				assert.NoError(t, err)

				value, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[s])
				assert.NoError(t, err)
				assert.Zero(t, value)
			})
		}
	})
}

func TestCodecDecodeNullGeometry(t *testing.T) {
	defaultConnTestRunner.RunTest(context.Background(), t, func(ctx context.Context, tb testing.TB, conn *pgx.Conn) {
		tb.Helper()
		rows, err := conn.Query(ctx, "select NULL::geometry", pgx.QueryResultFormats{pgx.BinaryFormatCode})
		assert.NoError(tb, err)

		for rows.Next() {
			values, err := rows.Values()
			assert.NoError(tb, err)
			assert.Equal(tb, []any{nil}, values)
		}

		assert.NoError(tb, rows.Err())
	})
}

func TestCodecScanValue(t *testing.T) {
	defaultConnTestRunner.RunTest(context.Background(), t, func(ctx context.Context, tb testing.TB, conn *pgx.Conn) {
		tb.Helper()
		for _, format := range []int16{
			pgx.BinaryFormatCode,
			pgx.TextFormatCode,
		} {
			tb.(*testing.T).Run(strconv.Itoa(int(format)), func(t *testing.T) {
				var geom geom.T
				err := conn.QueryRow(ctx, "select ST_SetSRID('POINT(1 2)'::geometry, 4326)", pgx.QueryResultFormats{format}).Scan(&geom)
				assert.NoError(t, err)
				assert.Equal(t, geom.FlatCoords(), []float64{1, 2})
			})
		}
	})
}

func TestCodecScanValuePolymorphic(t *testing.T) {
	defaultConnTestRunner.RunTest(context.Background(), t, func(ctx context.Context, tb testing.TB, conn *pgx.Conn) {
		tb.Helper()
		for _, format := range []int16{
			pgx.BinaryFormatCode,
			pgx.TextFormatCode,
		} {
			tb.(*testing.T).Run(strconv.Itoa(int(format)), func(t *testing.T) {
				var point geom.Point
				var polygon geom.Polygon
				var err error
				query := "select ST_SetSRID('POLYGON((0 0,1 0,1 1,0 1,0 0))'::geometry, 4326)"

				err = conn.QueryRow(ctx, query, pgx.QueryResultFormats{format}).Scan(&polygon)
				assert.NoError(t, err)
				assert.Equal(t, polygon.FlatCoords(), []float64{0, 0, 1, 0, 1, 1, 0, 1, 0, 0})

				err = conn.QueryRow(ctx, query, pgx.QueryResultFormats{format}).Scan(&point)
				assert.Error(t, err)
				var scanArgError pgx.ScanArgError
				assert.True(t, errors.As(err, &scanArgError))
				assert.Equal(t, scanArgError.Err.Error(), "pgxgeom: got *geom.Polygon, want *geom.Point")
			})
		}
	})
}

type CustomPoint struct {
	*geom.Point
}

var errCustomPointScan = errors.New("invalid target for CustomPoint")

func (c *CustomPoint) ScanGeom(v geom.T) error {
	concrete, ok := v.(*geom.Point)
	if !ok {
		return errCustomPointScan
	}
	c.Point = concrete
	return nil
}

func (c *CustomPoint) GeomValue() (geom.T, error) {
	return c.Point, nil
}

func TestCodecEncodeValueCustom(t *testing.T) {
	defaultConnTestRunner.RunTest(context.Background(), t, func(ctx context.Context, tb testing.TB, conn *pgx.Conn) {
		tb.Helper()
		point := CustomPoint{geom.NewPointFlat(geom.XY, []float64{1, 2}).SetSRID(4326)}

		var bytes []byte
		err := conn.QueryRow(ctx, "select $1::geometry::bytea", &point).Scan(&bytes)
		assert.NoError(t, err)

		g, err := ewkb.Unmarshal(bytes)
		assert.NoError(t, err)
		assert.Equal(t, g.(*geom.Point).FlatCoords(), []float64{1, 2})
	})
}

func TestCodecScanValueCustom(t *testing.T) {
	defaultConnTestRunner.RunTest(context.Background(), t, func(ctx context.Context, tb testing.TB, conn *pgx.Conn) {
		tb.Helper()
		for _, format := range []int16{
			pgx.BinaryFormatCode,
			pgx.TextFormatCode,
		} {
			tb.(*testing.T).Run(strconv.Itoa(int(format)), func(t *testing.T) {
				var point CustomPoint
				var err error
				pointQuery := "select ST_SetSRID('POINT(1 2)'::geometry, 4326)"
				polygonQuery := "select ST_SetSRID('POLYGON((0 0,1 0,1 1,0 1,0 0))'::geometry, 4326)"

				err = conn.QueryRow(ctx, pointQuery, pgx.QueryResultFormats{format}).Scan(&point)
				assert.NoError(t, err)
				assert.Equal(t, point.FlatCoords(), []float64{1, 2})

				err = conn.QueryRow(ctx, polygonQuery, pgx.QueryResultFormats{format}).Scan(&point)
				assert.Error(t, err)
				assert.Equal(t, err, error(pgx.ScanArgError{Err: errCustomPointScan}))
			})
		}
	})
}

func mustEWKB(tb testing.TB, g geom.T) []byte {
	tb.Helper()
	data, err := ewkb.Marshal(g, binary.LittleEndian)
	assert.NoError(tb, err)
	return data
}

func mustNewGeomFromWKT(tb testing.TB, s string, srid int) geom.T {
	tb.Helper()
	g, err := wkt.Unmarshal(s)
	assert.NoError(tb, err)
	g, err = geom.SetSRID(g, srid)
	assert.NoError(tb, err)
	return g
}
