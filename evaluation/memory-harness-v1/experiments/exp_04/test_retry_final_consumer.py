"""Run the original complete functional pipeline with predeclared v3 retries."""
import importlib.util
from pathlib import Path
import sys

HERE=Path(__file__).resolve().parent


if __name__=='__main__':
    if len(sys.argv)!=4 or sys.argv[1]!='--integration':raise SystemExit(2)
    spec=importlib.util.spec_from_file_location('_retry_full_final_consumer',HERE/'test_final_consumer.py')
    fixture=importlib.util.module_from_spec(spec);spec.loader.exec_module(fixture)
    fixture.integration(Path(sys.argv[2]),sys.argv[3],retries=True)
