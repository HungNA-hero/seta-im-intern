const fs = require('fs');

const graph = JSON.parse(fs.readFileSync('.understand-anything/intermediate/assembled-graph.json', 'utf8'));
const layers = JSON.parse(fs.readFileSync('.understand-anything/intermediate/layers.json', 'utf8'));
let readmeContent = '';
try {
  readmeContent = fs.readFileSync('README.md', 'utf8').substring(0, 3000);
} catch(e) {}

const fileLevelTypes = new Set(['file', 'config', 'document', 'service', 'pipeline', 'table', 'schema', 'resource', 'endpoint']);
const fileNodes = graph.nodes.filter(n => fileLevelTypes.has(n.type)).map(n => ({
  id: n.id, type: n.type, name: n.name, filePath: n.filePath, summary: n.summary, tags: n.tags
}));

let prompt = `Create a guided learning tour for this codebase.
Project root: c:\\Users\\admin\\Downloads\\Seta Im Intern\\seta-im-intern
Write output to: c:\\Users\\admin\\Downloads\\Seta Im Intern\\seta-im-intern\\.understand-anything\\intermediate\\tour.json

Additional Context:
Project README (first 3000 chars):
${readmeContent}

Project entry point: services/asset-core/cmd/server/main.go and services/access-core/src/index.ts

Nodes (all file-level nodes):
${JSON.stringify(fileNodes, null, 2)}

Edges (all edges):
${JSON.stringify(graph.edges, null, 2)}

Layers:
${JSON.stringify(layers, null, 2)}`;

fs.writeFileSync('.understand-anything/tmp/ua-tour-prompt.txt', prompt);
console.log('Prompt written. Size: ' + prompt.length);
