package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"tidegate/internal/cluster"
	"tidegate/internal/metrics"
	"tidegate/internal/store"
)

// PatchAgentSettingsRequest updates optional bandwidth and/or weight overrides on an agent.
type PatchAgentSettingsRequest struct {
	BandwidthBps *int64   `json:"bandwidth_bps,omitempty"`
	Weight       *float64 `json:"weight,omitempty"`
}

// JoinTokenCreated returns a newly issued plaintext join token alongside its metadata.
type JoinTokenCreated struct {
	Token string               `json:"token"`
	Item  store.AgentJoinToken `json:"item"`
}

// StepHistogramView reports fine-grained HDR histogram distribution for one step.
type StepHistogramView struct {
	ID      string                  `json:"id"`
	Name    string                  `json:"name"`
	IsGroup bool                    `json:"is_group,omitempty"`
	Latency metrics.LatencyStats    `json:"latency"`
	Bands   []int64                 `json:"bands"`
	Buckets []metrics.LatencyBucket `json:"buckets"`
}

// RunHistogramResponse summarizes whole-run and per-step latency distributions.
type RunHistogramResponse struct {
	RunID   string                  `json:"run_id"`
	Latency metrics.LatencyStats    `json:"latency"`
	Bands   []int64                 `json:"bands"`
	Buckets []metrics.LatencyBucket `json:"buckets"`
	Steps   []StepHistogramView     `json:"steps,omitempty"`
}

func hashJoinToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

func (c *Controller) initClusterCA(opts Options) (*cluster.ClusterCA, error) {
	if opts.DisableClusterTLS {
		return nil, nil
	}
	caDir := strings.TrimSpace(opts.ClusterCADir)
	if caDir == "" && opts.Store != nil {
		caDir = filepath.Dir(opts.Store.Root())
	}
	if caDir == "" {
		return cluster.NewEphemeralClusterCA()
	}
	return cluster.LoadOrCreateClusterCA(
		filepath.Join(caDir, "cluster-ca.pem"),
		filepath.Join(caDir, "cluster-ca.key"),
	)
}

func (c *Controller) clusterServerHosts(listenAddr string) []string {
	hosts := []string{"127.0.0.1", "::1", "localhost"}
	if h, _, err := net.SplitHostPort(listenAddr); err == nil && h != "" && h != "0.0.0.0" && h != "::" {
		hosts = append(hosts, h)
	}
	return hosts
}

// ClusterCAFingerprint returns the lowercase SHA-256 hex digest of the Cluster CA.
func (c *Controller) ClusterCAFingerprint() string {
	if c == nil || c.ca == nil {
		return ""
	}
	return c.ca.Fingerprint()
}

// ClusterCACertPEM returns the PEM-encoded Cluster CA root certificate.
func (c *Controller) ClusterCACertPEM() []byte {
	if c == nil || c.ca == nil {
		return nil
	}
	return c.ca.CertPEM()
}

func (c *Controller) publishAgentReject(nodeName, reason, message string) {
	c.hub.publish(Event{
		Type: EventAgentRejected,
		AgentRejected: &AgentRejectView{
			NodeName:      nodeName,
			ServerVersion: c.opts.Version,
			Reason:        reason,
			Message:       message,
			Time:          time.Now(),
		},
	})
}

func (c *Controller) handleEnroll(ctx context.Context, req *cluster.EnrollRequest) (*cluster.EnrollResponse, error) {
	reqName := ""
	if req != nil {
		reqName = req.Name
	}
	if c.ca == nil {
		msg := "控制器未启用集群 CA"
		c.publishAgentReject(reqName, "enroll_disabled", msg)
		return &cluster.EnrollResponse{RejectMessage: msg}, nil
	}
	if req == nil || strings.TrimSpace(req.Token) == "" {
		msg := "节点加入令牌不能为空"
		c.publishAgentReject(reqName, "bad_token", msg)
		return &cluster.EnrollResponse{RejectMessage: msg}, nil
	}

	authorized := cluster.TokenEqual(req.Token, c.internalToken) ||
		(c.opts.ClusterToken != "" && cluster.TokenEqual(req.Token, c.opts.ClusterToken))
	if !authorized {
		if _, err := c.results.ConsumeJoinToken(ctx, hashJoinToken(req.Token), time.Now()); err != nil {
			msg := "节点加入令牌无效、已被使用或已过期"
			if strings.Contains(err.Error(), "已") {
				msg = err.Error()
			}
			c.publishAgentReject(reqName, "bad_token", msg)
			return &cluster.EnrollResponse{RejectMessage: msg}, nil
		}
	}

	certPEM, serialHex, expiresAt, err := c.ca.SignCSR(req.CSRDER, req.Name, 365*24*time.Hour)
	if err != nil {
		c.publishAgentReject(reqName, "enroll_csr_error", err.Error())
		return &cluster.EnrollResponse{RejectMessage: err.Error()}, nil
	}
	if err := c.results.SaveAgentCert(ctx, serialHex, req.Name, expiresAt); err != nil {
		msg := fmt.Sprintf("记录节点证书失败：%v", err)
		c.publishAgentReject(reqName, "enroll_store_error", msg)
		return &cluster.EnrollResponse{RejectMessage: msg}, nil
	}
	return &cluster.EnrollResponse{
		ClientCertPEM: certPEM,
		CACertPEM:     c.ca.CertPEM(),
	}, nil
}

func (c *Controller) handleVerifyClientCert(cert *x509.Certificate, nodeID string) error {
	if cert == nil {
		return errors.New("缺少客户端证书")
	}
	revoked, err := c.results.IsAgentRevoked(c.ctx, cluster.CertSerialHex(cert), nodeID)
	if err != nil {
		return err
	}
	if revoked {
		return errors.New("节点或证书已被管理员吊销，禁止继续接入")
	}
	return nil
}

// CreateJoinToken mints a cluster agent enrollment token (`tgj_...`) stored as SHA-256.
func (c *Controller) CreateJoinToken(ctx context.Context, name, createdBy string, ttl time.Duration, singleUse bool) (*JoinTokenCreated, error) {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	var rnd [24]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return nil, fmt.Errorf("生成加入令牌失败：%w", err)
	}
	plain := "tgj_" + hex.EncodeToString(rnd[:])
	now := time.Now()
	tok := store.AgentJoinToken{
		Name:      strings.TrimSpace(name),
		SingleUse: singleUse,
		CreatedBy: createdBy,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
	if tok.Name == "" {
		tok.Name = "agent-join"
	}
	if err := c.results.CreateJoinToken(ctx, &tok, hashJoinToken(plain)); err != nil {
		return nil, err
	}
	return &JoinTokenCreated{Token: plain, Item: tok}, nil
}

// ListJoinTokens returns all saved agent join tokens without plaintext secrets.
func (c *Controller) ListJoinTokens(ctx context.Context) ([]store.AgentJoinToken, error) {
	return c.results.ListJoinTokens(ctx)
}

// DeleteJoinToken deletes an unused or expired join token by ID.
func (c *Controller) DeleteJoinToken(ctx context.Context, id string) error {
	return c.results.DeleteJoinToken(ctx, id)
}

// RevokeAgent revokes a connected or known agent's certificate and immediately disconnects it.
func (c *Controller) RevokeAgent(ctx context.Context, idOrName string) error {
	target := strings.TrimSpace(idOrName)
	if target == "" {
		return userErr(ErrInvalidInput, "节点标识不能为空", nil)
	}
	var nodeID, nodeName, certSerial string
	for _, n := range c.srv.Nodes() {
		if n.ID == target || n.Name == target {
			nodeID = n.ID
			nodeName = n.Name
			certSerial = n.CertSerial
			break
		}
	}
	if nodeID == "" {
		nodeName = target
	}
	if err := c.results.RevokeAgent(ctx, nodeID, nodeName, certSerial, "节点已被管理员吊销"); err != nil {
		return err
	}
	if nodeID != "" {
		c.srv.DisconnectNode(nodeID, "节点已被管理员吊销，禁止继续接入")
	}
	c.notifyAgentsChanged()
	return nil
}

// PatchAgentSettings updates an agent's bandwidth limit and/or custom load weight.
func (c *Controller) PatchAgentSettings(ctx context.Context, idOrName string, req PatchAgentSettingsRequest) (*AgentView, error) {
	targetName := strings.TrimSpace(idOrName)
	if targetName == "" {
		return nil, userErr(ErrInvalidInput, "节点标识不能为空", nil)
	}
	for _, n := range c.srv.Nodes() {
		if n.ID == targetName || n.Name == targetName {
			targetName = n.Name
			break
		}
	}
	cur, err := c.results.AgentSettings(ctx, targetName)
	if err != nil {
		return nil, err
	}
	if req.BandwidthBps != nil {
		if *req.BandwidthBps < 0 {
			return nil, userErr(ErrInvalidInput, "带宽上限不能为负数", nil)
		}
		cur.BandwidthBps = *req.BandwidthBps
	}
	if req.Weight != nil {
		w := *req.Weight
		if math.IsNaN(w) || math.IsInf(w, 0) || w < 0 {
			return nil, userErr(ErrInvalidInput, "节点权重必须是非负有限数字", nil)
		}
		cur.Weight = w
	}
	if err := c.results.SetAgentSettings(ctx, cur); err != nil {
		return nil, err
	}
	c.notifyAgentsChanged()

	agents, err := c.Agents(ctx)
	if err != nil {
		return nil, err
	}
	for i := range agents {
		if agents[i].ID == idOrName || agents[i].Name == targetName {
			return &agents[i], nil
		}
	}
	return &AgentView{
		ID:           idOrName,
		Name:         targetName,
		BandwidthBps: cur.BandwidthBps,
		Weight:       cur.Weight,
	}, nil
}

func effectiveNodeWeight(ni cluster.NodeInfo, s store.AgentSettings) float64 {
	if s.Weight > 0 {
		return s.Weight
	}
	if ni.CPUCores > 0 {
		return float64(ni.CPUCores)
	}
	return 1
}

func matchNodeSelector(labels, selector map[string]string) bool {
	for k, want := range selector {
		tk := strings.TrimSpace(k)
		if tk == "" {
			continue
		}
		got, ok := labels[tk]
		if !ok || got != want {
			return false
		}
	}
	return true
}

func filterNodesBySelector(nodes []cluster.NodeInfo, selector map[string]string) []cluster.NodeInfo {
	if len(selector) == 0 {
		return nodes
	}
	var out []cluster.NodeInfo
	for _, n := range nodes {
		if matchNodeSelector(n.Labels, selector) {
			out = append(out, n)
		}
	}
	return out
}

func formatSelectorDesc(selector map[string]string) string {
	keys := make([]string, 0, len(selector))
	for k := range selector {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, selector[k]))
	}
	return strings.Join(parts, ",")
}

func buildNodeSnapshotView(agentID, agentName string, snap *metrics.Snapshot) metrics.NodeSnapshotView {
	v := snap.View()
	return metrics.NodeSnapshotView{
		AgentID:     agentID,
		AgentName:   agentName,
		ActiveVUs:   v.ActiveVUs,
		ActiveConns: v.ActiveConns,
		Target:      v.Target,
		Requests:    v.Requests,
		RPS:         v.RPS,
		Errors:      v.Errors,
		ErrorRate:   v.ErrorRate,
		P50Ms:       v.Latency.P50,
		P95Ms:       v.Latency.P95,
		P99Ms:       v.Latency.P99,
		SentBps:     v.SentBps,
		ReceivedBps: v.ReceivedBps,
	}
}

// RunHistogram returns HDR latency distribution buckets for total and per-step histograms.
func (c *Controller) RunHistogram(ctx context.Context, runID string) (*RunHistogramResponse, error) {
	c.mu.Lock()
	ar := c.active
	if ar != nil && ar.id != runID {
		ar = nil
	}
	c.mu.Unlock()

	var snap *metrics.Snapshot
	if ar != nil {
		snap = ar.snapshotClone()
		if snap == nil {
			return &RunHistogramResponse{
				RunID:   runID,
				Bands:   make([]int64, 12),
				Buckets: []metrics.LatencyBucket{},
				Steps:   []StepHistogramView{},
			}, nil
		}
	}
	if snap == nil {
		loaded, err := c.results.MergedSnapshot(ctx, runID)
		if err != nil {
			return nil, err
		}
		snap = loaded
	}

	out := &RunHistogramResponse{
		RunID:   runID,
		Latency: snap.Total.Latency.Stats(),
		Bands:   snap.Total.Latency.FixedBands(),
		Buckets: snap.Total.Latency.Distribution(24),
		Steps:   make([]StepHistogramView, 0, len(snap.Steps)),
	}
	for _, st := range snap.Steps {
		out.Steps = append(out.Steps, StepHistogramView{
			ID:      st.ID,
			Name:    st.Name,
			IsGroup: st.IsGroup,
			Latency: st.Latency.Stats(),
			Bands:   st.Latency.FixedBands(),
			Buckets: st.Latency.Distribution(24),
		})
	}
	return out, nil
}
