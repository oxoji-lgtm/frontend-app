import logging
import os
import sys

from flask import Flask, render_template

app = Flask(__name__)

# Set up logging
logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

# Define a view function
@app.route('/')
def index():
    return render_template('index.html')

if __name__ == '__main__':
    # Bind to port 5000 on localhost
    app.run(debug=True, host='0.0.0.0', port=5000)