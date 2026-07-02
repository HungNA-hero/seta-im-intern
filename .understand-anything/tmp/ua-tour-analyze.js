const fs = require('fs');
const path = require('path');

const inputPath = process.argv[2];
const outputPath = process.argv[3];

if (!inputPath || !outputPath) {
    console.error("Usage: node ua-tour-analyze.js <input.json> <output.json>");
    process.exit(1);
}

const data = JSON.parse(fs.readFileSync(inputPath, 'utf8'));
const nodes = data.nodes || [];
const edges = data.edges || [];
const layers = data.layers || [];

const nodeSummaryIndex = {};
nodes.forEach(n => {
    nodeSummaryIndex[n.id] = {
        name: n.name,
        type: n.type,
        summary: n.summary,
        filePath: n.filePath
    };
});

const fanIn = {};
const fanOut = {};
const graph = {};
const undirectedGraph = {};

nodes.forEach(n => {
    fanIn[n.id] = 0;
    fanOut[n.id] = 0;
    graph[n.id] = [];
    undirectedGraph[n.id] = new Set();
});

edges.forEach(e => {
    const s = e.source;
    const t = e.target;
    if (fanIn[t] !== undefined) fanIn[t]++;
    if (fanOut[s] !== undefined) fanOut[s]++;

    if ((e.type === 'imports' || e.type === 'calls') && graph[s]) {
        graph[s].push(t);
    }

    if (e.type === 'imports' || e.type === 'calls') {
        if (undirectedGraph[s] && undirectedGraph[t]) {
            undirectedGraph[s].add(t);
            undirectedGraph[t].add(s);
        }
    }
});

const fanInRanking = nodes.map(n => ({ id: n.id, fanIn: fanIn[n.id], name: n.name }))
    .sort((a, b) => b.fanIn - a.fanIn)
    .slice(0, 20);

const fanOutRanking = nodes.map(n => ({ id: n.id, fanOut: fanOut[n.id], name: n.name }))
    .sort((a, b) => b.fanOut - a.fanOut)
    .slice(0, 20);

const fanOutValues = Object.values(fanOut);
fanOutValues.sort((a, b) => a - b);
const fanOutTop10PercentThreshold = fanOutValues[Math.floor(fanOutValues.length * 0.9)] || 0;

const fanInValues = Object.values(fanIn);
fanInValues.sort((a, b) => a - b);
const fanInBottom25PercentThreshold = fanInValues[Math.floor(fanInValues.length * 0.25)] || 0;

const entryPointCandidates = [];
const entryPointRegex = /^(index|main|app|server|mod|manage|wsgi|asgi|run|__main__|Application|Main|Program|config|App)\.(ts|js|rs|go|py|java|cs|ru|php|swift|kt|cpp|c)$/i;

nodes.forEach(n => {
    let score = 0;
    if (n.type === 'file' || n.type === 'script') {
        const nameOrPath = n.filePath || n.name || n.id;
        const name = path.basename(nameOrPath);
        if (entryPointRegex.test(name)) score += 3;

        const depth = nameOrPath.split('/').length;
        if (depth <= 2) score += 1;

        if (fanOut[n.id] >= fanOutTop10PercentThreshold && fanOut[n.id] > 0) score += 1;
        if (fanIn[n.id] <= fanInBottom25PercentThreshold) score += 1;

        if (score > 0) entryPointCandidates.push({ id: n.id, score, name: n.name, summary: n.summary, type: n.type });
    } else if (n.type === 'document' && (n.name || '').toLowerCase() === 'readme.md') {
        const depth = (n.filePath || n.name || n.id).split('/').length;
        if (depth === 1) {
            score += 5;
        } else {
            score += 2;
        }
        entryPointCandidates.push({ id: n.id, score, name: n.name, summary: n.summary, type: n.type });
    } else if (n.type === 'document' && (n.name || '').toLowerCase().endsWith('.md')) {
        const depth = (n.filePath || n.name || n.id).split('/').length;
        if (depth === 1) {
            score += 2;
            entryPointCandidates.push({ id: n.id, score, name: n.name, summary: n.summary, type: n.type });
        }
    }
});

entryPointCandidates.sort((a, b) => b.score - a.score);
const top5Candidates = entryPointCandidates.slice(0, 5);

let startNode = null;
for (const cand of entryPointCandidates) {
    if (cand.type !== 'document') {
        startNode = cand.id;
        break;
    }
}
if (!startNode && entryPointCandidates.length > 0) {
    const anyCodeFile = nodes.find(n => n.type === 'file' || n.type === 'script');
    if (anyCodeFile) startNode = anyCodeFile.id;
}

const bfsTraversal = {
    startNode,
    order: [],
    depthMap: {},
    byDepth: {}
};

if (startNode) {
    const queue = [{ id: startNode, depth: 0 }];
    const visited = new Set([startNode]);

    while (queue.length > 0) {
        const curr = queue.shift();
        bfsTraversal.order.push(curr.id);
        bfsTraversal.depthMap[curr.id] = curr.depth;

        if (!bfsTraversal.byDepth[curr.depth]) {
            bfsTraversal.byDepth[curr.depth] = [];
        }
        bfsTraversal.byDepth[curr.depth].push(curr.id);

        const neighbors = graph[curr.id] || [];
        for (const neighbor of neighbors) {
            if (!visited.has(neighbor)) {
                visited.add(neighbor);
                queue.push({ id: neighbor, depth: curr.depth + 1 });
            }
        }
    }
}

const nonCodeFiles = {
    documentation: [],
    infrastructure: [],
    data: [],
    config: []
};

nodes.forEach(n => {
    const item = { id: n.id, name: n.name, type: n.type, summary: n.summary };
    if (n.type === 'document') {
        nonCodeFiles.documentation.push(item);
    } else if (['service', 'pipeline', 'resource'].includes(n.type)) {
        nonCodeFiles.infrastructure.push(item);
    } else if (['table', 'schema', 'endpoint'].includes(n.type)) {
        nonCodeFiles.data.push(item);
    } else if (n.type === 'config') {
        nonCodeFiles.config.push(item);
    }
});

const bidirEdges = [];
edges.forEach(e => {
    if (e.type === 'imports' || e.type === 'calls') {
        const rev = edges.find(r => r.source === e.target && r.target === e.source && (r.type === 'imports' || r.type === 'calls'));
        if (rev && e.source < e.target) {
            bidirEdges.push([e.source, e.target]);
        }
    }
});

let clusters = [];
const inCluster = new Set();
const clusterGraph = {};
bidirEdges.forEach(([u, v]) => {
    if (!clusterGraph[u]) clusterGraph[u] = [];
    if (!clusterGraph[v]) clusterGraph[v] = [];
    clusterGraph[u].push(v);
    clusterGraph[v].push(u);
});

Object.keys(clusterGraph).forEach(node => {
    if (!inCluster.has(node)) {
        const cluster = [];
        const q = [node];
        inCluster.add(node);
        while(q.length > 0) {
            const curr = q.shift();
            cluster.push(curr);
            (clusterGraph[curr] || []).forEach(neighbor => {
                if (!inCluster.has(neighbor)) {
                    inCluster.add(neighbor);
                    q.push(neighbor);
                }
            });
        }
        if (cluster.length >= 2) {
            clusters.push(cluster);
        }
    }
});

clusters = clusters.map(c => {
    const clusterSet = new Set(c);
    let expanded = true;
    while(expanded) {
        expanded = false;
        nodes.forEach(n => {
            if (!clusterSet.has(n.id)) {
                let connections = 0;
                clusterSet.forEach(member => {
                    if (undirectedGraph[n.id] && undirectedGraph[n.id].has(member)) {
                        connections++;
                    }
                });
                if (connections >= 2) {
                    clusterSet.add(n.id);
                    expanded = true;
                }
            }
        });
    }
    return Array.from(clusterSet);
});

const formattedClusters = clusters.map(c => {
    let edgeCount = 0;
    c.forEach(u => {
        c.forEach(v => {
            if (u !== v && undirectedGraph[u] && undirectedGraph[u].has(v)) {
                edgeCount++;
            }
        });
    });
    return {
        nodes: c,
        edgeCount: edgeCount / 2
    };
}).sort((a, b) => b.nodes.length - a.nodes.length).slice(0, 10);

const layerList = {
    count: layers.length,
    list: layers
};

const output = {
    scriptCompleted: true,
    entryPointCandidates: top5Candidates,
    fanInRanking,
    fanOutRanking,
    bfsTraversal,
    nonCodeFiles,
    clusters: formattedClusters,
    layers: layerList,
    nodeSummaryIndex,
    totalNodes: nodes.length,
    totalEdges: edges.length
};

fs.writeFileSync(outputPath, JSON.stringify(output, null, 2), 'utf8');
console.log("Analysis complete.");
