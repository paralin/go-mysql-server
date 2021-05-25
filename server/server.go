// Copyright 2020-2021 Dolthub, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	sqle "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/auth"
	"github.com/dolthub/vitess/go/mysql"
	"github.com/opentracing/opentracing-go"
	"github.com/sirupsen/logrus"
)

// Server is a MySQL server for SQLe engines.
type Server struct {
	Listener *mysql.Listener
	h        *Handler
}

// Config for the mysql server.
type Config struct {
	// Auth of the server.
	Auth auth.Auth
	// Tracer to use in the server. By default, a noop tracer will be used if
	// no tracer is provided.
	Tracer opentracing.Tracer
	// Version string to advertise in running server
	Version string
	// Logger is the logger to use, otherwise uses stderr.
	Logger *logrus.Entry
}

// NewDefaultServer creates a Server with the default session builder.
func NewDefaultServer(cfg Config, e *sqle.Engine) (*Server, error) {
	return NewServer(cfg, e, DefaultSessionBuilder)
}

// NewServer creates a server with the given protocol, address, authentication
// details given a SQLe engine and a session builder.
func NewServer(cfg Config, e *sqle.Engine, sb SessionBuilder) (*Server, error) {
	var tracer opentracing.Tracer
	if cfg.Tracer != nil {
		tracer = cfg.Tracer
	} else {
		tracer = opentracing.NoopTracer{}
	}

	le := buildDefaultLogger(cfg.Logger)

	serverAddr := "127.0.0.1" // placeholder
	handler := NewHandler(
		le,
		e,
		NewSessionManager(
			sb,
			tracer,
			e.Catalog.HasDB,
			e.Catalog.MemoryManager,
			serverAddr,
		),
	)
	var a mysql.AuthServer
	if cfg.Auth != nil {
		a = cfg.Auth.Mysql()
	}
	if a == nil {
		a = (&auth.None{}).Mysql()
	}
	vtListnr, err := mysql.NewListener(a, handler)
	if err != nil {
		return nil, err
	}

	if cfg.Version != "" {
		vtListnr.ServerVersion = cfg.Version
	}

	return &Server{Listener: vtListnr, h: handler}, nil
}
