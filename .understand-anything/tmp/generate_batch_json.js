const fs = require('fs');
const path = require('path');

const projectRoot = 'c:/Users/admin/Downloads/Seta Im Intern/seta-im-intern';
const intermediateDir = path.join(projectRoot, '.understand-anything', 'intermediate');
const tmpDir = path.join(projectRoot, '.understand-anything', 'tmp');

const BATCHES = [1, 2, 3];

const typeMapping = {
  code: 'file',
  config: 'config',
  docs: 'document',
  script: 'file',
  markup: 'file'
};

function getInfraNodeType(filePath) {
  if (/docker-compose/i.test(filePath) || /Dockerfile/i.test(filePath)) return 'service';
  if (/\.github\/workflows/i.test(filePath) || /\.gitlab-ci/i.test(filePath)) return 'pipeline';
  return 'resource';
}

function getDataNodeType(filePath) {
  if (/\.sql$/i.test(filePath)) return 'table';
  if (/\.graphql$/i.test(filePath) || /\.proto$/i.test(filePath) || /\.prisma$/i.test(filePath)) return 'schema';
  if (/openapi/i.test(filePath) || /swagger/i.test(filePath)) return 'endpoint';
  return 'schema';
}

function getNodeType(fileCategory, filePath) {
  if (fileCategory === 'infra') return getInfraNodeType(filePath);
  if (fileCategory === 'data') return getDataNodeType(filePath);
  return typeMapping[fileCategory] || 'file';
}

function getSummary(fileCategory, filePath) {
  switch (fileCategory) {
    case 'config': return `Configuration file for ${path.basename(filePath)}.`;
    case 'docs': return `Documentation for ${path.basename(filePath)}.`;
    case 'infra': return `Infrastructure definition in ${path.basename(filePath)}.`;
    case 'data': return `Data schema or definition in ${path.basename(filePath)}.`;
    case 'script': return `Executable script ${path.basename(filePath)}.`;
    default: return `Source code file ${path.basename(filePath)}.`;
  }
}

function getTags(fileCategory, filePath, structure) {
  let tags = [];
  switch (fileCategory) {
    case 'code':
      if (/test|spec/.test(filePath)) tags.push('test');
      if (/index\.(ts|js)|main\.(go|rs)|__init__\.py/.test(filePath)) tags.push('entry-point');
      if (structure && structure.exports && structure.exports.length > 0) {
        if (structure.functions && structure.functions.length === 0) tags.push('barrel');
        else tags.push('utility');
      } else {
        tags.push('component');
      }
      break;
    case 'config': tags.push('configuration'); break;
    case 'docs': tags.push('documentation'); break;
    case 'infra': tags.push('infrastructure'); break;
    case 'data': tags.push('schema-definition'); break;
    case 'script': tags.push('utility'); break;
    default: tags.push('component');
  }
  if (tags.length < 3) {
    tags.push('module', 'file');
  }
  return tags.slice(0, 5);
}

function getComplexity(nonEmptyLines) {
  if (nonEmptyLines < 50) return 'simple';
  if (nonEmptyLines < 200) return 'moderate';
  return 'complex';
}

BATCHES.forEach(batchIndex => {
  const inputPath = path.join(tmpDir, `ua-file-analyzer-input-${batchIndex}.json`);
  const resultPath = path.join(tmpDir, `ua-file-extract-results-${batchIndex}.json`);

  if (!fs.existsSync(inputPath) || !fs.existsSync(resultPath)) return;

  const inputData = JSON.parse(fs.readFileSync(inputPath, 'utf8'));
  const resultData = JSON.parse(fs.readFileSync(resultPath, 'utf8'));

  const nodes = [];
  const edges = [];
  const fileMap = {};
  inputData.batchFiles.forEach(f => { fileMap[f.path] = f; });

  const functionNodesMap = {}; // name -> full id within the same file

  resultData.results.forEach(res => {
    const fData = fileMap[res.path] || { fileCategory: 'code', sizeLines: res.totalLines };
    const nodeType = getNodeType(fData.fileCategory, res.path);
    const nodeId = `${nodeType}:${res.path}`;

    nodes.push({
      id: nodeId,
      type: nodeType,
      name: path.basename(res.path),
      filePath: res.path,
      summary: getSummary(fData.fileCategory, res.path),
      tags: getTags(fData.fileCategory, res.path, res),
      complexity: getComplexity(res.nonEmptyLines || 0)
    });

    const fileFunctions = new Set();
    const fileClasses = new Set();

    if (res.functions) {
      res.functions.forEach(fn => {
        if (fn.endLine - fn.startLine >= 10 || (res.exports && res.exports.some(e => e.name === fn.name))) {
          const fnId = `function:${res.path}:${fn.name}`;
          fileFunctions.add(fn.name);
          functionNodesMap[`${res.path}:${fn.name}`] = fnId;
          nodes.push({
            id: fnId,
            type: 'function',
            name: fn.name,
            filePath: res.path,
            summary: `Function ${fn.name} in ${path.basename(res.path)}.`,
            tags: ['function', 'utility', 'code'],
            complexity: getComplexity(fn.endLine - fn.startLine),
            lineRange: [fn.startLine, fn.endLine]
          });
          edges.push({ source: nodeId, target: fnId, type: 'contains', direction: 'forward', weight: 1.0, ownerFilePath: res.path });
          if (res.exports && res.exports.some(e => e.name === fn.name)) {
            edges.push({ source: nodeId, target: fnId, type: 'exports', direction: 'forward', weight: 0.8, ownerFilePath: res.path });
          }
        }
      });
    }

    if (res.classes) {
      res.classes.forEach(cls => {
        if (cls.endLine - cls.startLine >= 20 || (cls.methods && cls.methods.length >= 2) || (res.exports && res.exports.some(e => e.name === cls.name))) {
          const clsId = `class:${res.path}:${cls.name}`;
          fileClasses.add(cls.name);
          nodes.push({
            id: clsId,
            type: 'class',
            name: cls.name,
            filePath: res.path,
            summary: `Class ${cls.name} in ${path.basename(res.path)}.`,
            tags: ['class', 'data-model', 'code'],
            complexity: getComplexity(cls.endLine - cls.startLine),
            lineRange: [cls.startLine, cls.endLine]
          });
          edges.push({ source: nodeId, target: clsId, type: 'contains', direction: 'forward', weight: 1.0, ownerFilePath: res.path });
          if (res.exports && res.exports.some(e => e.name === cls.name)) {
            edges.push({ source: nodeId, target: clsId, type: 'exports', direction: 'forward', weight: 0.8, ownerFilePath: res.path });
          }
        }
      });
    }

    if (res.services) {
      res.services.forEach(svc => {
        const svcId = `service:${res.path}:${svc.name}`;
        nodes.push({ id: svcId, type: 'service', name: svc.name, filePath: res.path, summary: `Service ${svc.name} defined in ${path.basename(res.path)}.`, tags: ['service', 'infrastructure', 'containerization'], complexity: 'simple' });
        edges.push({ source: nodeId, target: svcId, type: 'contains', direction: 'forward', weight: 1.0, ownerFilePath: res.path });
      });
    }

    if (res.endpoints) {
      res.endpoints.forEach(ep => {
        const epId = `endpoint:${res.path}:${ep.name}`;
        nodes.push({ id: epId, type: 'endpoint', name: ep.name, filePath: res.path, summary: `Endpoint ${ep.name} defined in ${path.basename(res.path)}.`, tags: ['endpoint', 'api-schema', 'schema-definition'], complexity: 'simple' });
        edges.push({ source: nodeId, target: epId, type: 'contains', direction: 'forward', weight: 1.0, ownerFilePath: res.path });
      });
    }

    if (res.definitions) {
      res.definitions.forEach(def => {
        const defId = `schema:${res.path}:${def.name}`;
        nodes.push({ id: defId, type: 'schema', name: def.name, filePath: res.path, summary: `Schema definition ${def.name}.`, tags: ['schema-definition', 'data', 'definition'], complexity: 'simple' });
        edges.push({ source: nodeId, target: defId, type: 'contains', direction: 'forward', weight: 1.0, ownerFilePath: res.path });
      });
    }

    if (res.resources) {
      res.resources.forEach(r => {
        const rId = `resource:${res.path}:${r.name}`;
        nodes.push({ id: rId, type: 'resource', name: r.name, filePath: res.path, summary: `Resource ${r.name} defined in ${path.basename(res.path)}.`, tags: ['resource', 'infrastructure', 'deployment'], complexity: 'simple' });
        edges.push({ source: nodeId, target: rId, type: 'contains', direction: 'forward', weight: 1.0, ownerFilePath: res.path });
      });
    }

    // Imports
    const imports = inputData.batchImportData[res.path] || [];
    imports.forEach(imp => {
      edges.push({ source: nodeId, target: `file:${imp}`, type: 'imports', direction: 'forward', weight: 0.7, ownerFilePath: res.path });
    });

    // Call Graph (within the same file)
    if (res.callGraph) {
      res.callGraph.forEach(cg => {
        if (fileFunctions.has(cg.caller) && fileFunctions.has(cg.callee)) {
          edges.push({
            source: `function:${res.path}:${cg.caller}`,
            target: `function:${res.path}:${cg.callee}`,
            type: 'calls',
            direction: 'forward',
            weight: 0.8,
            ownerFilePath: res.path
          });
        }
      });
    }

    // tested_by (simplistic inference based on filename)
    if (/test|spec/.test(res.path)) {
      imports.forEach(imp => {
        edges.push({
          source: `file:${imp}`,
          target: nodeId,
          type: 'tested_by',
          direction: 'forward',
          weight: 0.5,
          ownerFilePath: res.path
        });
      });
    }
  });

  const cleanEdges = edges.map(e => {
    const ce = { ...e };
    delete ce.ownerFilePath;
    return ce;
  });

  const outPath = path.join(intermediateDir, `batch-${batchIndex}.json`);
  if (nodes.length <= 60 && edges.length <= 120) {
    fs.writeFileSync(outPath, JSON.stringify({ nodes, edges: cleanEdges }, null, 2));
    console.log(`Wrote batch-${batchIndex}.json`);
  } else {
    const parts = Math.ceil(Math.max(nodes.length / 60, edges.length / 120));
    const files = [...new Set(nodes.map(n => n.filePath))].sort();

    for (let k = 1; k <= parts; k++) {
      const startIdx = Math.floor((k - 1) * files.length / parts);
      const endIdx = Math.floor(k * files.length / parts);
      const partFiles = new Set(files.slice(startIdx, endIdx));

      const partNodes = nodes.filter(n => partFiles.has(n.filePath));
      const partEdges = edges.filter(e => partFiles.has(e.ownerFilePath)).map(e => {
        const ce = { ...e };
        delete ce.ownerFilePath;
        return ce;
      });

      fs.writeFileSync(
        path.join(intermediateDir, `batch-${batchIndex}-part-${k}.json`),
        JSON.stringify({ nodes: partNodes, edges: partEdges }, null, 2)
      );
      console.log(`Wrote batch-${batchIndex}-part-${k}.json`);
    }
  }
});
