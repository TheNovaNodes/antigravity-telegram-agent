def calculate_value():
    return 3 # BUG! Should return 4

def test_artificial_failure():
    assert calculate_value() == 4
