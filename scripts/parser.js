const fs = require('fs');
const path = require('path');

class Parser {
    constructor(filePath) {
        this.filePath = path.resolve(filePath);
    }

    readFile() {
        return new Promise((resolve, reject) => {
            fs.readFile(this.filePath, 'utf8', (err, data) => {
                if (err) {
                    reject(err);
                } else {
                    resolve(data);
                }
            });
        });
    }

    parseJSON() {
        return this.readFile().then(data => {
            try {
                return JSON.parse(data);
            } catch (err) {
                throw new Error('Invalid JSON format');
            }
        });
    }

    parseCSV() {
        return this.readFile().then(data => {
            const lines = data.split('\n');
            const headers = lines[0].split(',');
            const result = [];

            for (let i = 1; i < lines.length; i++) {
                const obj = {};
                const currentLine = lines[i].split(',');

                if (currentLine.length === headers.length) {
                    headers.forEach((header, index) => {
                        obj[header.trim()] = currentLine[index].trim();
                    });
                    result.push(obj);
                }
            }

            return result;
        });
    }
}

module.exports = Parser;