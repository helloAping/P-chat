const { Document, Packer, Paragraph, TextRun } = require('docx');
const fs = require('fs');

const doc = new Document({
  sections: [{
    children: [
      new Paragraph({
        children: [new TextRun("你好")]
      })
    ]
  }]
});

Packer.toBuffer(doc).then(buffer => {
  const targetPath = process.argv[2] || "1.docx";
  fs.writeFileSync(targetPath, buffer);
  console.log("OK: " + targetPath);
});
