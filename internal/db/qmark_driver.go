package db

import (
	"context"
	"database/sql"
	"database/sql/driver"

	"github.com/jackc/pgx/v5/stdlib"
)

func init() {
	sql.Register("pgx-qmark", &qmarkDriver{inner: stdlib.GetDefaultDriver()})
}

// qmarkDriver wraps pgx's database/sql driver and rewrites ? placeholders.
type qmarkDriver struct {
	inner driver.Driver
}

func (d *qmarkDriver) Open(name string) (driver.Conn, error) {
	c, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return wrapConn(c), nil
}

func (d *qmarkDriver) OpenConnector(name string) (driver.Connector, error) {
	dc, ok := d.inner.(driver.DriverContext)
	if !ok {
		return &dsnConnector{dsn: name, driver: d}, nil
	}
	c, err := dc.OpenConnector(name)
	if err != nil {
		return nil, err
	}
	return &qmarkConnector{inner: c}, nil
}

type dsnConnector struct {
	dsn    string
	driver driver.Driver
}

func (c *dsnConnector) Connect(context.Context) (driver.Conn, error) {
	return c.driver.Open(c.dsn)
}

func (c *dsnConnector) Driver() driver.Driver { return c.driver }

type qmarkConnector struct {
	inner driver.Connector
}

func (c *qmarkConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return wrapConn(conn), nil
}

func (c *qmarkConnector) Driver() driver.Driver {
	return &qmarkDriver{inner: c.inner.Driver()}
}

func wrapConn(c driver.Conn) driver.Conn {
	return &qmarkConn{Conn: c}
}

type qmarkConn struct {
	driver.Conn
}

func (c *qmarkConn) Prepare(query string) (driver.Stmt, error) {
	return c.Conn.Prepare(Rebind(query))
}

func (c *qmarkConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if pc, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return pc.PrepareContext(ctx, Rebind(query))
	}
	return c.Prepare(query)
}

func (c *qmarkConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if ec, ok := c.Conn.(driver.ExecerContext); ok {
		return ec.ExecContext(ctx, Rebind(query), args)
	}
	return nil, driver.ErrSkip
}

func (c *qmarkConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if qc, ok := c.Conn.(driver.QueryerContext); ok {
		return qc.QueryContext(ctx, Rebind(query), args)
	}
	return nil, driver.ErrSkip
}

func (c *qmarkConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if bt, ok := c.Conn.(driver.ConnBeginTx); ok {
		return bt.BeginTx(ctx, opts)
	}
	return c.Conn.Begin()
}

func (c *qmarkConn) Ping(ctx context.Context) error {
	if p, ok := c.Conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *qmarkConn) ResetSession(ctx context.Context) error {
	if r, ok := c.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *qmarkConn) IsValid() bool {
	if v, ok := c.Conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

func (c *qmarkConn) CheckNamedValue(nv *driver.NamedValue) error {
	if nvc, ok := c.Conn.(driver.NamedValueChecker); ok {
		return nvc.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}
