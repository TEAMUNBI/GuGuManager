package store

import (
	"time"

	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/id"
)

func (m *Memory) seed() error {
	now := time.Now().UTC().Truncate(time.Second)

	m.addNode(domain.Node{
		ID: "11111111-1111-4111-8111-111111111111", Name: "nimbus-east-01", Condition: "available", Version: "agent 0.1.0-dev",
		Region: "Shanghai / East", Address: "10.0.10.21", LastHeartbeatAt: now.Add(-8 * time.Second), CPUCores: 16,
		MemoryBytes: 68_719_476_736, DiskBytes: 1_099_511_627_776, AllocatedMemoryBytes: 38_654_705_664,
		AllocatedDiskBytes: 461_708_984_320, RunningServers: 6, TotalServers: 9,
		Capabilities: []string{"container/v1", "console/v1", "backup/v1", "metrics/v1"},
	})
	m.addNode(domain.Node{
		ID: "22222222-2222-4222-8222-222222222222", Name: "atlas-edge-02", Condition: "available", Version: "agent 0.1.0-dev",
		Region: "Singapore / Edge", Address: "10.0.20.14", LastHeartbeatAt: now.Add(-12 * time.Second), CPUCores: 8,
		MemoryBytes: 34_359_738_368, DiskBytes: 549_755_813_888, AllocatedMemoryBytes: 17_179_869_184,
		AllocatedDiskBytes: 239_075_328_000, RunningServers: 3, TotalServers: 5,
		Capabilities: []string{"container/v1", "console/v1", "metrics/v1"},
	})
	m.addNode(domain.Node{
		ID: "33333333-3333-4333-8333-333333333333", Name: "harbor-lab-03", Condition: "offline", Version: "agent 0.0.9",
		Region: "Tokyo / Lab", Address: "10.0.30.18", LastHeartbeatAt: now.Add(-3 * time.Minute), CPUCores: 8,
		MemoryBytes: 34_359_738_368, DiskBytes: 549_755_813_888, AllocatedMemoryBytes: 8_589_934_592,
		AllocatedDiskBytes: 107_374_182_400, RunningServers: 1, TotalServers: 2,
		Capabilities: []string{"container/v1", "console/v1"},
	})

	games, err := loadFixedGameCatalog()
	if err != nil {
		return err
	}
	for _, game := range games {
		m.addGame(game)
	}
	paper := m.games["io.gugumanager.papermc"]
	factorio := m.games["io.gugumanager.factorio"]
	vintageStory := m.games["io.gugumanager.vintagestory"]

	m.addServer(domain.Server{
		ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Name: "雾港生存服", Description: "Season 04 / whitelist", GameID: paper.ID, GameBundleDigest: paper.BundleDigest, GameDefinitionVersion: paper.Version, GameName: paper.Name, GameVersion: paper.GameVersion,
		NodeID: "11111111-1111-4111-8111-111111111111", NodeName: "nimbus-east-01", LifecycleState: "ready", DesiredPower: "running", ObservedPower: "running", NodeCondition: "available", HealthCondition: "healthy", Generation: 12, ObservedGeneration: 12, ObservedAt: now.Add(-11 * time.Second), Allocation: "10.0.10.21:25565", OwnerName: "Liang Chen",
		Metrics: domain.ResourceMetrics{CPUPercent: 42, MemoryBytes: 3_221_225_472, MemoryLimit: 4_294_967_296, DiskBytes: 18_420_000_000, DiskLimit: 26_843_545_600, NetworkRxBytes: 3_100_000_000, NetworkTxBytes: 1_800_000_000, PlayersOnline: 18, PlayersCapacity: 40}, MetricHistory: history(now, 42, 3_221_225_472), UpdatedAt: now.Add(-11 * time.Second),
	})
	m.addServer(domain.Server{
		ID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Name: "周五工厂", Description: "Co-op / autosave enabled", GameID: factorio.ID, GameBundleDigest: factorio.BundleDigest, GameDefinitionVersion: factorio.Version, GameName: factorio.Name, GameVersion: factorio.GameVersion,
		NodeID: "22222222-2222-4222-8222-222222222222", NodeName: "atlas-edge-02", LifecycleState: "ready", DesiredPower: "stopped", ObservedPower: "stopped", NodeCondition: "available", HealthCondition: "unknown", Generation: 7, ObservedGeneration: 7, ObservedAt: now.Add(-2 * time.Minute), Allocation: "10.0.20.14:34197", OwnerName: "Mina Wu",
		Metrics: domain.ResourceMetrics{CPUPercent: 0, MemoryBytes: 1_020_000_000, MemoryLimit: 4_294_967_296, DiskBytes: 12_500_000_000, DiskLimit: 21_474_836_480, NetworkRxBytes: 0, NetworkTxBytes: 0, PlayersOnline: 0, PlayersCapacity: 12}, MetricHistory: history(now, 0, 1_020_000_000), UpdatedAt: now.Add(-2 * time.Minute),
	})
	m.addServer(domain.Server{
		ID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", Name: "Aurora Isles", Description: "Experimental / node reconnect required", GameID: vintageStory.ID, GameBundleDigest: vintageStory.BundleDigest, GameDefinitionVersion: vintageStory.Version, GameName: vintageStory.Name, GameVersion: vintageStory.GameVersion,
		NodeID: "33333333-3333-4333-8333-333333333333", NodeName: "harbor-lab-03", LifecycleState: "ready", DesiredPower: "running", ObservedPower: "unknown", NodeCondition: "offline", HealthCondition: "unknown", Generation: 3, ObservedGeneration: 2, ObservedAt: now.Add(-3 * time.Minute), Allocation: "10.0.30.18:42420", OwnerName: "Kai Zhou",
		Metrics: domain.ResourceMetrics{CPUPercent: 0, MemoryBytes: 0, MemoryLimit: 3_221_225_472, DiskBytes: 8_600_000_000, DiskLimit: 19_327_352_832, PlayersOnline: 0, PlayersCapacity: 8}, MetricHistory: history(now, 16, 1_700_000_000), UpdatedAt: now.Add(-3 * time.Minute),
	})

	seededAllocations := []domain.Allocation{
		{ID: "a1111111-1111-4111-8111-111111111111", ServerID: m.serverOrder[0], NodeID: "11111111-1111-4111-8111-111111111111", BindIP: "10.0.10.21", Port: 25565, Protocol: "tcp", Primary: true, CreatedAt: now.Add(-day(30)), UpdatedAt: now.Add(-11 * time.Second)},
		{ID: "a2222222-2222-4222-8222-222222222222", ServerID: m.serverOrder[1], NodeID: "22222222-2222-4222-8222-222222222222", BindIP: "10.0.20.14", Port: 34197, Protocol: "udp", Primary: true, CreatedAt: now.Add(-day(14)), UpdatedAt: now.Add(-2 * time.Minute)},
		{ID: "a3333333-3333-4333-8333-333333333333", ServerID: m.serverOrder[2], NodeID: "33333333-3333-4333-8333-333333333333", BindIP: "10.0.30.18", Port: 42420, Protocol: "tcp", Primary: true, CreatedAt: now.Add(-day(7)), UpdatedAt: now.Add(-3 * time.Minute)},
	}
	for _, allocation := range seededAllocations {
		m.allocations[allocation.ID] = allocation
		m.allocationOrder[allocation.ServerID] = append(m.allocationOrder[allocation.ServerID], allocation.ID)
	}
	startupOverrides := map[string]map[string]any{
		m.serverOrder[0]: {"rcon_password": "paper-rcon-seeded"},
		m.serverOrder[1]: {"server_token": "factorio-token-seeded"},
	}
	for _, serverID := range m.serverOrder {
		server := m.servers[serverID]
		startup, values, err := startupFromFixedBundle(server, m.games[server.GameID], startupOverrides[serverID])
		if err != nil {
			return err
		}
		m.startups[serverID] = startup
		m.startupValues[serverID] = values
	}

	m.console[m.serverOrder[0]] = []domain.ConsoleLine{
		{Sequence: 1841, Timestamp: now.Add(-44 * time.Second), Stream: "system", Message: "[panel] attached to console stream (development adapter)"},
		{Sequence: 1842, Timestamp: now.Add(-38 * time.Second), Stream: "stdout", Message: "[18:24:11 INFO]: Done (4.892s)! For help, type \"help\""},
		{Sequence: 1843, Timestamp: now.Add(-21 * time.Second), Stream: "stdout", Message: "[18:24:28 INFO]: [Server thread/INFO]: There are 18 of a max of 40 players online"},
		{Sequence: 1844, Timestamp: now.Add(-8 * time.Second), Stream: "stdout", Message: "[18:24:41 INFO]: Saving the game (this may take a moment!)"},
	}
	m.files[m.serverOrder[0]] = []domain.FileEntry{
		{Name: "config", Path: "config", Kind: "directory", SizeBytes: 0, ModifiedAt: now.Add(-8 * time.Hour)},
		{Name: "logs", Path: "logs", Kind: "directory", SizeBytes: 0, ModifiedAt: now.Add(-11 * time.Minute)},
		{Name: "world", Path: "world", Kind: "directory", SizeBytes: 0, ModifiedAt: now.Add(-3 * time.Minute)},
		{Name: "server.properties", Path: "server.properties", Kind: "file", SizeBytes: 2940, ModifiedAt: now.Add(-2 * time.Hour)},
		{Name: "eula.txt", Path: "eula.txt", Kind: "file", SizeBytes: 11, ModifiedAt: now.Add(-4 * time.Hour)},
		{Name: "config/paper-global.yml", Path: "config/paper-global.yml", Kind: "file", SizeBytes: 912, ModifiedAt: now.Add(-8 * time.Hour)},
		{Name: "logs/latest.log", Path: "logs/latest.log", Kind: "file", SizeBytes: 84120, ModifiedAt: now.Add(-11 * time.Minute)},
	}
	m.files[m.serverOrder[1]] = []domain.FileEntry{
		{Name: "saves", Path: "saves", Kind: "directory", SizeBytes: 0, ModifiedAt: now.Add(-5 * time.Hour)},
		{Name: "server-settings.json", Path: "server-settings.json", Kind: "file", SizeBytes: 810, ModifiedAt: now.Add(-5 * time.Hour)},
		{Name: "saves/world.zip", Path: "saves/world.zip", Kind: "file", SizeBytes: 12_500_000_000, ModifiedAt: now.Add(-2 * time.Hour)},
	}
	m.files[m.serverOrder[2]] = []domain.FileEntry{{Name: "data", Path: "data", Kind: "directory", SizeBytes: 0, ModifiedAt: now.Add(-24 * time.Hour)}}

	m.backups[m.serverOrder[0]] = []domain.Backup{
		{ID: "d1111111-1111-4111-8111-111111111111", Name: "pre-season-04", Status: "ready", SizeBytes: 8_420_000_000, Checksum: "sha256:5c7a5c7a5c7a5c7a5c7a5c7a5c7a5c7a5c7a5c7a5c7a5c7a5c7a5c7a5c7a5c7a", CreatedAt: now.Add(-25 * time.Hour)},
		{ID: "d2222222-2222-4222-8222-222222222222", Name: "before-config-tune", Status: "ready", SizeBytes: 8_190_000_000, Checksum: "sha256:8a028a028a028a028a028a028a028a028a028a028a028a028a028a028a028a02", CreatedAt: now.Add(-day(4))},
	}
	m.backups[m.serverOrder[1]] = []domain.Backup{{ID: "d3333333-3333-4333-8333-333333333333", Name: "autosave-2026-08-06", Status: "ready", SizeBytes: 7_420_000_000, Checksum: "sha256:30c230c230c230c230c230c230c230c230c230c230c230c230c230c230c230c2", CreatedAt: now.Add(-20 * time.Hour)}}
	m.backups[m.serverOrder[2]] = nil

	m.audit = []domain.AuditEvent{
		{ID: id.New(), ActorName: "GuGu Admin", Action: "server.power.start", TargetType: "server", TargetName: "雾港生存服", Result: "success", OperationID: "e1111111-1111-4111-8111-111111111111", CreatedAt: now.Add(-11 * time.Second)},
		{ID: id.New(), ActorName: "Mina Wu", Action: "backup.create", TargetType: "server", TargetName: "周五工厂", Result: "success", OperationID: "e2222222-2222-4222-8222-222222222222", CreatedAt: now.Add(-21 * time.Minute)},
		{ID: id.New(), ActorName: "GuGu Admin", Action: "catalog.approve", TargetType: "game_definition", TargetName: "PaperMC 1.0.0", Result: "success", OperationID: "e3333333-3333-4333-8333-333333333333", CreatedAt: now.Add(-2 * time.Hour)},
		{ID: id.New(), ActorName: "GuGu Admin", Action: "node.heartbeat", TargetType: "node", TargetName: "atlas-edge-02", Result: "success", OperationID: "e4444444-4444-4444-8444-444444444444", CreatedAt: now.Add(-2 * time.Hour)},
		{ID: id.New(), ActorName: "System", Action: "server.reconcile", TargetType: "server", TargetName: "Aurora Isles", Result: "failure", OperationID: "e5555555-5555-4555-8555-555555555555", CreatedAt: now.Add(-3 * time.Hour)},
	}
	return nil
}

func day(days int) time.Duration { return time.Duration(days) * 24 * time.Hour }

func history(now time.Time, cpu float64, memory int64) []domain.MetricPoint {
	points := make([]domain.MetricPoint, 0, 12)
	for i := 11; i >= 0; i-- {
		wave := float64((i*7)%13) - 6
		points = append(points, domain.MetricPoint{Timestamp: now.Add(-time.Duration(i*5) * time.Minute), CPUPercent: maxFloat(0, cpu+wave), MemoryBytes: memory + int64(i%4)*83_000_000})
	}
	return points
}

func maxFloat(left float64, right float64) float64 {
	if left > right {
		return left
	}
	return right
}

func (m *Memory) addNode(node domain.Node) {
	m.nodes[node.ID] = node
	m.nodeOrder = append(m.nodeOrder, node.ID)
}

func (m *Memory) addGame(game domain.GameDefinition) {
	m.games[game.ID] = game
	m.gameOrder = append(m.gameOrder, game.ID)
}

func (m *Memory) addServer(server domain.Server) {
	m.servers[server.ID] = server
	m.serverOrder = append(m.serverOrder, server.ID)
}
