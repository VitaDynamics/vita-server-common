package nacos

//grpc client 连接池的方案，如果使用链接池，需要考虑连接的健康状态和重连机制，场景会更复杂。
//目前时间紧，任务重。grpc client使用连接复用的方式，后续可以根据实际使用情况进行优化和完善。

// import (
// 	"context"
// 	"fmt"
// 	"sync"
// 	"sync/atomic"
// 	"time"

// 	"github.com/sirupsen/logrus"
// 	"google.golang.org/grpc"
// 	"google.golang.org/grpc/connectivity"
// 	"google.golang.org/grpc/credentials/insecure"
// 	"google.golang.org/grpc/keepalive"
// )

// // grpc client 连接池的方式
// // PoolConfig defines configuration for the gRPC connection pool
// type PoolConfig struct {
// 	NacosConf           *PkgNacosConfig
// 	NamespaceID         string
// 	ServiceName         string
// 	GroupName           string        // optional, defaults to DefaultNacosGroup
// 	ClusterName         string        // optional
// 	MinConnections      int           // minimum connections to maintain (default: 2)
// 	MaxConnections      int           // maximum connections to allow (default: 10)
// 	IdleTimeout         time.Duration // close idle connections after this time (default: 5 minutes)
// 	HealthCheckInterval time.Duration // how often to check connection health (default: 30 seconds)
// }

// // GrpcConnectionPool maintains a pool of reusable gRPC connections to a Nacos-registered service.
// // It supports multiple concurrent connections, automatic failover, and connection health monitoring.
// type GrpcConnectionPool struct {
// 	mu                  sync.RWMutex
// 	conns               []*pooledConnection // slice of pooled connections
// 	config              PoolConfig
// 	nextConnIndex       uint32                         // for round-robin load balancing
// 	connCreatedTime     map[*grpc.ClientConn]time.Time // track connection creation time
// 	lastHealthCheckTime time.Time
// 	closed              bool
// 	ctx                 context.Context
// 	cancel              context.CancelFunc
// 	wg                  sync.WaitGroup
// }

// // pooledConnection wraps a gRPC connection with metadata
// type pooledConnection struct {
// 	conn       *grpc.ClientConn
// 	createdAt  time.Time
// 	lastUsedAt time.Time
// 	mu         sync.Mutex
// }

// // NewGrpcConnectionPool creates a new connection pool with specified configuration
// func NewGrpcConnectionPool(cfg PoolConfig) (*GrpcConnectionPool, error) {
// 	// Set defaults
// 	if cfg.MinConnections <= 0 {
// 		cfg.MinConnections = 2
// 	}
// 	if cfg.MaxConnections <= 0 {
// 		cfg.MaxConnections = 10
// 	}
// 	if cfg.MaxConnections < cfg.MinConnections {
// 		return nil, fmt.Errorf("maxConnections (%d) must be >= minConnections (%d)", cfg.MaxConnections, cfg.MinConnections)
// 	}
// 	if cfg.IdleTimeout == 0 {
// 		cfg.IdleTimeout = 5 * time.Minute
// 	}
// 	if cfg.HealthCheckInterval == 0 {
// 		cfg.HealthCheckInterval = 30 * time.Second
// 	}
// 	if cfg.GroupName == "" {
// 		cfg.GroupName = DefaultNacosGroup
// 	}

// 	// Validate required fields
// 	if cfg.NacosConf == nil {
// 		return nil, fmt.Errorf("nacos config is required")
// 	}
// 	if cfg.ServiceName == "" {
// 		return nil, fmt.Errorf("service name is required")
// 	}

// 	ctx, cancel := context.WithCancel(context.Background())
// 	pool := &GrpcConnectionPool{
// 		config:          cfg,
// 		conns:           make([]*pooledConnection, 0, cfg.MinConnections),
// 		connCreatedTime: make(map[*grpc.ClientConn]time.Time),
// 		ctx:             ctx,
// 		cancel:          cancel,
// 	}

// 	// Initialize minimum connections
// 	for i := 0; i < cfg.MinConnections; i++ {
// 		if err := pool.createConnection(context.Background()); err != nil {
// 			pool.Close()
// 			return nil, fmt.Errorf("failed to initialize connection %d: %w", i+1, err)
// 		}
// 	}

// 	// Start background health check goroutine
// 	pool.wg.Add(1)
// 	go pool.healthCheckLoop()

// 	logrus.WithFields(logrus.Fields{
// 		"service":   cfg.ServiceName,
// 		"min_conns": cfg.MinConnections,
// 		"max_conns": cfg.MaxConnections,
// 	}).Info("gRPC connection pool initialized")

// 	return pool, nil
// }

// // GetConnection returns a connection from the pool using round-robin load balancing
// func (p *GrpcConnectionPool) GetConnection(ctx context.Context) (*grpc.ClientConn, error) {
// 	p.mu.RLock()
// 	if p.closed {
// 		p.mu.RUnlock()
// 		return nil, fmt.Errorf("connection pool is closed")
// 	}
// 	connCount := len(p.conns)
// 	p.mu.RUnlock()

// 	if connCount == 0 {
// 		return nil, fmt.Errorf("no connections available in pool")
// 	}

// 	// Round-robin selection
// 	index := atomic.AddUint32(&p.nextConnIndex, 1) % uint32(connCount)

// 	p.mu.RLock()
// 	pooledConn := p.conns[index]
// 	p.mu.RUnlock()

// 	pooledConn.mu.Lock()
// 	pooledConn.lastUsedAt = time.Now()
// 	pooledConn.mu.Unlock()

// 	// Check if connection is healthy, if not try to get another
// 	if conn := pooledConn.conn; conn.GetState() == connectivity.Shutdown {
// 		logrus.WithFields(logrus.Fields{
// 			"service": p.config.ServiceName,
// 			"index":   index,
// 		}).Warn("connection is shutdown, attempting to reconnect")

// 		if err := p.reconnectConnection(pooledConn); err != nil {
// 			// Try to get a different connection
// 			return p.GetConnectionWithFallback(ctx)
// 		}
// 	}

// 	return pooledConn.conn, nil
// }

// // GetConnectionWithFallback tries to find a healthy connection or creates a new one
// func (p *GrpcConnectionPool) GetConnectionWithFallback(ctx context.Context) (*grpc.ClientConn, error) {
// 	p.mu.RLock()
// 	if p.closed {
// 		p.mu.RUnlock()
// 		return nil, fmt.Errorf("connection pool is closed")
// 	}

// 	// Try to find a healthy connection
// 	for _, pooledConn := range p.conns {
// 		if pooledConn.conn.GetState() != connectivity.Shutdown {
// 			pooledConn.mu.Lock()
// 			pooledConn.lastUsedAt = time.Now()
// 			pooledConn.mu.Unlock()
// 			p.mu.RUnlock()
// 			return pooledConn.conn, nil
// 		}
// 	}
// 	p.mu.RUnlock()

// 	// Try to create a new connection if under limit
// 	p.mu.Lock()
// 	if len(p.conns) < p.config.MaxConnections {
// 		p.mu.Unlock()
// 		if err := p.createConnection(ctx); err == nil {
// 			return p.GetConnection(ctx)
// 		}
// 	} else {
// 		p.mu.Unlock()
// 	}

// 	return nil, fmt.Errorf("no healthy connections available and pool is at max capacity")
// }

// // createConnection establishes a new gRPC connection and adds it to the pool
// func (p *GrpcConnectionPool) createConnection(ctx context.Context) error {
// 	addr, err := resolveGrpcAddrFromNacos(p.config.NacosConf, p.config.NamespaceID, p.config.ServiceName, p.config.GroupName, p.config.ClusterName)
// 	if err != nil {
// 		logrus.WithFields(logrus.Fields{
// 			"service": p.config.ServiceName,
// 			"error":   err,
// 		}).Error("failed to resolve grpc address from nacos")
// 		return err
// 	}

// 	conn, err := grpc.NewClient(addr,
// 		grpc.WithTransportCredentials(insecure.NewCredentials()),
// 		grpc.WithKeepaliveParams(defaultGrpcKeepaliveParams()),
// 	)
// 	if err != nil {
// 		logrus.WithFields(logrus.Fields{
// 			"service": p.config.ServiceName,
// 			"addr":    addr,
// 			"error":   err,
// 		}).Error("failed to create grpc client")
// 		return err
// 	}

// 	now := time.Now()
// 	pooledConn := &pooledConnection{
// 		conn:       conn,
// 		createdAt:  now,
// 		lastUsedAt: now,
// 	}

// 	p.mu.Lock()
// 	p.conns = append(p.conns, pooledConn)
// 	p.connCreatedTime[conn] = now
// 	p.mu.Unlock()

// 	logrus.WithFields(logrus.Fields{
// 		"service":   p.config.ServiceName,
// 		"addr":      addr,
// 		"pool_size": len(p.conns),
// 	}).Debug("new connection created and added to pool")

// 	return nil
// }

// // reconnectConnection replaces a broken connection with a new one
// func (p *GrpcConnectionPool) reconnectConnection(pooledConn *pooledConnection) error {
// 	pooledConn.mu.Lock()
// 	oldConn := pooledConn.conn
// 	pooledConn.mu.Unlock()

// 	// Create new connection
// 	addr, err := resolveGrpcAddrFromNacos(p.config.NacosConf, p.config.NamespaceID, p.config.ServiceName, p.config.GroupName, p.config.ClusterName)
// 	if err != nil {
// 		return err
// 	}

// 	newConn, err := grpc.NewClient(addr,
// 		grpc.WithTransportCredentials(insecure.NewCredentials()),
// 		grpc.WithKeepaliveParams(defaultGrpcKeepaliveParams()),
// 	)
// 	if err != nil {
// 		return err
// 	}

// 	pooledConn.mu.Lock()
// 	pooledConn.conn = newConn
// 	pooledConn.createdAt = time.Now()
// 	pooledConn.lastUsedAt = time.Now()
// 	pooledConn.mu.Unlock()

// 	// Close old connection asynchronously
// 	go func() {
// 		_ = oldConn.Close()
// 		p.mu.Lock()
// 		delete(p.connCreatedTime, oldConn)
// 		p.mu.Unlock()
// 	}()

// 	logrus.WithFields(logrus.Fields{
// 		"service": p.config.ServiceName,
// 		"addr":    addr,
// 	}).Info("connection reconnected")

// 	return nil
// }

// // healthCheckLoop periodically checks and maintains pool health
// func (p *GrpcConnectionPool) healthCheckLoop() {
// 	defer p.wg.Done()

// 	ticker := time.NewTicker(p.config.HealthCheckInterval)
// 	defer ticker.Stop()

// 	for {
// 		select {
// 		case <-p.ctx.Done():
// 			return
// 		case <-ticker.C:
// 			p.performHealthCheck()
// 		}
// 	}
// }

// // performHealthCheck checks all connections and removes idle ones
// func (p *GrpcConnectionPool) performHealthCheck() {
// 	p.mu.Lock()
// 	defer p.mu.Unlock()

// 	now := time.Now()
// 	healthyConns := make([]*pooledConnection, 0, len(p.conns))

// 	for _, pooledConn := range p.conns {
// 		pooledConn.mu.Lock()
// 		lastUsed := pooledConn.lastUsedAt
// 		createdAt := pooledConn.createdAt
// 		conn := pooledConn.conn
// 		pooledConn.mu.Unlock()

// 		state := conn.GetState()

// 		// Remove dead connections
// 		if state == connectivity.Shutdown {
// 			logrus.WithFields(logrus.Fields{
// 				"service": p.config.ServiceName,
// 			}).Debug("removing shutdown connection from pool")
// 			_ = conn.Close()
// 			delete(p.connCreatedTime, conn)
// 			continue
// 		}

// 		// Remove idle connections (but keep minimum)
// 		if now.Sub(lastUsed) > p.config.IdleTimeout && len(healthyConns)+1 > p.config.MinConnections {
// 			logrus.WithFields(logrus.Fields{
// 				"service":   p.config.ServiceName,
// 				"idle_time": now.Sub(lastUsed).Seconds(),
// 			}).Debug("removing idle connection from pool")
// 			_ = conn.Close()
// 			delete(p.connCreatedTime, conn)
// 			continue
// 		}

// 		// Log connection status
// 		logrus.WithFields(logrus.Fields{
// 			"service":  p.config.ServiceName,
// 			"state":    state.String(),
// 			"age_sec":  now.Sub(createdAt).Seconds(),
// 			"idle_sec": now.Sub(lastUsed).Seconds(),
// 		}).Debug("connection health check")

// 		healthyConns = append(healthyConns, pooledConn)
// 	}

// 	// Update pool with healthy connections
// 	p.conns = healthyConns

// 	// Restore minimum connections if needed
// 	for len(p.conns) < p.config.MinConnections {
// 		if err := p.createConnection(context.Background()); err != nil {
// 			logrus.WithFields(logrus.Fields{
// 				"service": p.config.ServiceName,
// 				"error":   err,
// 			}).Warn("failed to restore minimum connections")
// 			break
// 		}
// 	}

// 	logrus.WithFields(logrus.Fields{
// 		"service":   p.config.ServiceName,
// 		"pool_size": len(p.conns),
// 		"min_conns": p.config.MinConnections,
// 		"max_conns": p.config.MaxConnections,
// 	}).Debug("health check completed")
// }

// // Stats returns current pool statistics
// func (p *GrpcConnectionPool) Stats() map[string]interface{} {
// 	p.mu.RLock()
// 	defer p.mu.RUnlock()

// 	healthyCount := 0
// 	for _, pooledConn := range p.conns {
// 		if pooledConn.conn.GetState() != connectivity.Shutdown {
// 			healthyCount++
// 		}
// 	}

// 	return map[string]interface{}{
// 		"service":       p.config.ServiceName,
// 		"total_conns":   len(p.conns),
// 		"healthy_conns": healthyCount,
// 		"min_conns":     p.config.MinConnections,
// 		"max_conns":     p.config.MaxConnections,
// 		"next_conn_idx": atomic.LoadUint32(&p.nextConnIndex),
// 	}
// }

// // Close gracefully closes all connections in the pool
// func (p *GrpcConnectionPool) Close() error {
// 	p.mu.Lock()
// 	if p.closed {
// 		p.mu.Unlock()
// 		return nil
// 	}
// 	p.closed = true
// 	conns := p.conns
// 	p.conns = make([]*pooledConnection, 0)
// 	p.mu.Unlock()

// 	// Signal health check loop to stop
// 	p.cancel()

// 	// Close all connections
// 	for _, pooledConn := range conns {
// 		_ = pooledConn.conn.Close()
// 	}

// 	// Wait for goroutines to finish
// 	p.wg.Wait()

// 	logrus.WithFields(logrus.Fields{
// 		"service": p.config.ServiceName,
// 	}).Info("connection pool closed")

// 	return nil
// }
