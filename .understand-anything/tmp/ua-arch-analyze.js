const fs = require('fs');
const path = require('path');

const inputFile = process.argv[2];
const outputFile = process.argv[3];

if (!inputFile || !outputFile) {
  console.error("Usage: node analyze.js <input> <output>");
  process.exit(1);
}

const data = JSON.parse(fs.readFileSync(inputFile, 'utf-8'));
const { fileNodes, importEdges, allEdges } = data;

// Find common prefix
const filePaths = fileNodes.map(n => {
  const p = n.filePath || n.path || n.file;
  return p ? p.replace(/\\/g, '/') : '';
}).filter(p => p !== '');
let commonPrefix = '';
if (filePaths.length > 0) {
  const parts = filePaths[0].split('/');
  for (let i = 0; i < parts.length - 1; i++) {
    const prefix = parts.slice(0, i + 1).join('/') + '/';
    if (filePaths.every(p => p.startsWith(prefix))) {
      commonPrefix = prefix;
    } else {
      break;
    }
  }
}

// Group files by first directory after common prefix
const directoryGroups = {};
const nodeTypeGroups = {};

fileNodes.forEach(node => {
  const p = node.filePath || node.path || node.file || '';
  const filePath = p.replace(/\\/g, '/');
  let relPath = filePath.startsWith(commonPrefix) ? filePath.slice(commonPrefix.length) : filePath;

  let group;
  if (relPath.includes('/')) {
    group = relPath.split('/')[0];
  } else {
    if (/\.(test|spec)\.[a-z]+$/.test(relPath) || relPath.endsWith('Test.java')) {
      group = 'test';
    } else if (/\.config\./.test(relPath) || ['tsconfig.json', 'package.json'].includes(relPath)) {
      group = 'config';
    } else {
      group = 'root';
    }
  }

  if (!directoryGroups[group]) directoryGroups[group] = [];
  directoryGroups[group].push(node.id);

  const type = node.type || 'file';
  if (!nodeTypeGroups[type]) nodeTypeGroups[type] = [];
  nodeTypeGroups[type].push(node.id);
});

// Calculate edges
const crossCategoryMap = {};
const interGroupMap = {};
const groupEdgesMap = {};

Object.keys(directoryGroups).forEach(g => {
  groupEdgesMap[g] = { internal: 0, total: 0 };
});

const fileToGroup = {};
Object.entries(directoryGroups).forEach(([g, ids]) => {
  ids.forEach(id => fileToGroup[id] = g);
});

const idToNode = {};
fileNodes.forEach(n => idToNode[n.id] = n);

const fileFanIn = {};
const fileFanOut = {};

if (allEdges) {
  allEdges.forEach(edge => {
    const sourceNode = idToNode[edge.source];
    const targetNode = idToNode[edge.target];
    if (sourceNode && targetNode) {
      const sType = sourceNode.type || 'file';
      const tType = targetNode.type || 'file';

      const key = `${sType}->${tType}:${edge.type}`;
      if (!crossCategoryMap[key]) crossCategoryMap[key] = { fromType: sType, toType: tType, edgeType: edge.type, count: 0 };
      crossCategoryMap[key].count++;
    }
  });
}

const processImport = (source, target) => {
  if (!fileFanOut[source]) fileFanOut[source] = 0;
  if (!fileFanIn[target]) fileFanIn[target] = 0;
  fileFanOut[source]++;
  fileFanIn[target]++;

  const sGroup = fileToGroup[source];
  const tGroup = fileToGroup[target];

  if (sGroup && tGroup) {
    if (sGroup === tGroup) {
      groupEdgesMap[sGroup].internal++;
      groupEdgesMap[sGroup].total++;
    } else {
      groupEdgesMap[sGroup].total++;
      groupEdgesMap[tGroup].total++;

      const key = `${sGroup}->${tGroup}`;
      if (!interGroupMap[key]) interGroupMap[key] = { from: sGroup, to: tGroup, count: 0 };
      interGroupMap[key].count++;
    }
  }
};

if (importEdges) {
  importEdges.forEach(e => processImport(e.source, e.target));
} else if (allEdges) {
  allEdges.filter(e => e.type === 'imports' || e.type === 'depends_on').forEach(e => processImport(e.source, e.target));
}

const intraGroupDensity = {};
Object.keys(groupEdgesMap).forEach(g => {
  const { internal, total } = groupEdgesMap[g];
  intraGroupDensity[g] = {
    internalEdges: internal,
    totalEdges: total,
    density: total > 0 ? (internal / total) : 0
  };
});

const interGroupImports = Object.values(interGroupMap);
const crossCategoryEdges = Object.values(crossCategoryMap);

const patternMatches = {};
const patterns = [
  { p: /^(routes|api|controllers|endpoints|handlers|serializers|blueprints|routers)$/i, label: 'api' },
  { p: /^(services|core|lib|domain|logic|signals|internal|composables|mailers|jobs|channels|src\/main\/java)$/i, label: 'service' },
  { p: /^(models|db|data|persistence|repository|entities|migrations|entity|sql|database|schema)$/i, label: 'data' },
  { p: /^(components|views|pages|ui|layouts|screens)$/i, label: 'ui' },
  { p: /^(middleware|plugins|interceptors|guards)$/i, label: 'middleware' },
  { p: /^(utils|helpers|common|shared|tools|pkg|templatetags)$/i, label: 'utility' },
  { p: /^(config|constants|env|settings|management|commands)$/i, label: 'config' },
  { p: /^(__tests__|test|tests|spec|specs|src\/test\/java)$/i, label: 'test' },
  { p: /^(types|interfaces|schemas|contracts|dtos|dto|request|response)$/i, label: 'types' },
  { p: /^(hooks)$/i, label: 'hooks' },
  { p: /^(store|state|reducers|actions|slices)$/i, label: 'state' },
  { p: /^(assets|static|public)$/i, label: 'assets' },
  { p: /^(cmd|bin)$/i, label: 'entry' },
  { p: /^(docs|documentation|wiki)$/i, label: 'documentation' },
  { p: /^(deploy|deployment|infra|infrastructure|k8s|kubernetes|helm|charts|terraform|tf|docker)$/i, label: 'infrastructure' },
  { p: /^(\.github|\.gitlab|\.circleci)$/i, label: 'ci-cd' },
];

Object.keys(directoryGroups).forEach(g => {
  for (let p of patterns) {
    if (p.p.test(g)) {
      patternMatches[g] = p.label;
      break;
    }
  }
});

fileNodes.forEach(node => {
  const g = fileToGroup[node.id];
  const p = node.filePath || node.path || node.file || '';
  const fname = p.replace(/\\/g, '/').split('/').pop();
  if (!patternMatches[g] || patternMatches[g] === 'unknown') {
    if (/\.(test|spec)\.[a-z]+$/.test(fname) || /_test\.go$/.test(fname) || /Test\.java$/.test(fname)) patternMatches[g] = 'test';
    if (/\.d\.ts$/.test(fname)) patternMatches[g] = 'types';
    if (/\.sql$/.test(fname)) patternMatches[g] = 'data';
    if (/Dockerfile|docker-compose/.test(fname)) patternMatches[g] = 'infrastructure';
  }
});

const getPath = n => n.filePath || n.path || n.file || '';
const deploymentTopology = {
  hasDockerfile: fileNodes.some(n => /Dockerfile/.test(getPath(n))),
  hasCompose: fileNodes.some(n => /docker-compose/.test(getPath(n))),
  hasK8s: fileNodes.some(n => /\/(k8s|kubernetes|helm|charts)\//.test(getPath(n))),
  hasTerraform: fileNodes.some(n => /\.tf$/.test(getPath(n))),
  hasCI: fileNodes.some(n => /\.(github|gitlab|circleci)\//.test(getPath(n))),
  infraFiles: fileNodes.filter(n => /(Dockerfile|docker-compose|\.tf$|kubernetes|helm|github\/workflows)/.test(getPath(n))).map(getPath)
};

const dataPipeline = {
  schemaFiles: fileNodes.filter(n => /\.(sql|graphql|proto)$/.test(getPath(n))).map(getPath),
  migrationFiles: fileNodes.filter(n => /migrations?\//.test(getPath(n))).map(getPath),
  dataModelFiles: fileNodes.filter(n => /models?\//.test(getPath(n))).map(getPath),
  apiHandlerFiles: fileNodes.filter(n => /(routes?|api|controllers?)\//.test(getPath(n))).map(getPath)
};

const groupsWithDocs = Object.keys(directoryGroups).filter(g => {
  const files = directoryGroups[g];
  return files.some(id => {
      const node = idToNode[id];
      const p = getPath(node);
      return node.type === 'document' || p.endsWith('.md');
  });
});

const docCoverage = {
  groupsWithDocs: groupsWithDocs.length,
  totalGroups: Object.keys(directoryGroups).length,
  coverageRatio: Object.keys(directoryGroups).length ? groupsWithDocs.length / Object.keys(directoryGroups).length : 0,
  undocumentedGroups: Object.keys(directoryGroups).filter(g => !groupsWithDocs.includes(g))
};

const dependencyDirection = [];
Object.keys(interGroupMap).forEach(key => {
  const e1 = interGroupMap[key];
  const revKey = `${e1.to}->${e1.from}`;
  const e2 = interGroupMap[revKey];
  if (!e2 || e1.count > e2.count) {
    dependencyDirection.push({ dependent: e1.from, dependsOn: e1.to });
  } else if (e2 && e1.count === e2.count) {
    // tie
  }
});

const fileStats = {
  totalFileNodes: fileNodes.length,
  filesPerGroup: Object.fromEntries(Object.entries(directoryGroups).map(([g, ids]) => [g, ids.length])),
  nodeTypeCounts: Object.fromEntries(Object.entries(nodeTypeGroups).map(([t, ids]) => [t, ids.length]))
};

const results = {
  scriptCompleted: true,
  directoryGroups,
  nodeTypeGroups,
  crossCategoryEdges,
  interGroupImports,
  intraGroupDensity,
  patternMatches,
  deploymentTopology,
  dataPipeline,
  docCoverage,
  dependencyDirection,
  fileStats,
  fileFanIn,
  fileFanOut
};

fs.writeFileSync(outputFile, JSON.stringify(results, null, 2));
