const fs = require('fs');
const data = JSON.parse(fs.readFileSync('c:/Users/admin/Downloads/Seta Im Intern/seta-im-intern/.understand-anything/intermediate/batches.json', 'utf8'));

[0, 1, 2, 3].forEach(i => {
  const batch = data.batches.find(b => b.batchIndex === i);
  if (batch) {
    fs.writeFileSync(
      `c:/Users/admin/Downloads/Seta Im Intern/seta-im-intern/.understand-anything/tmp/ua-file-analyzer-input-${i}.json`,
      JSON.stringify({
        projectRoot: 'c:/Users/admin/Downloads/Seta Im Intern/seta-im-intern',
        batchFiles: batch.files,
        batchImportData: batch.batchImportData,
        neighborMap: batch.neighborMap || {}
      }, null, 2)
    );
    console.log(`Wrote input for batch ${i}`);
  } else {
    console.log(`Batch ${i} not found`);
  }
});
